package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Thunderstore struct {
	community string
	demo      bool
	client    *http.Client

	mu          sync.Mutex
	known       map[string]Package
	searchCache map[string]cachedSearch
}

type cachedSearch struct {
	at     time.Time
	result SearchResult
}

type cyberstormListing struct {
	Count    int                     `json:"count"`
	Next     string                  `json:"next"`
	Previous string                  `json:"previous"`
	Results  []cyberstormListingItem `json:"results"`
}

type cyberstormListingItem struct {
	CommunityIdentifier string `json:"community_identifier"`
	Description         string `json:"description"`
	IconURL             string `json:"icon_url"`
	Name                string `json:"name"`
	Namespace           string `json:"namespace"`
	LastUpdated         string `json:"last_updated"`
	DownloadCount       int    `json:"download_count"`
}

type experimentalPackage struct {
	Namespace    string                     `json:"namespace"`
	Name         string                     `json:"name"`
	FullName     string                     `json:"full_name"`
	Owner        string                     `json:"owner"`
	PackageURL   string                     `json:"package_url"`
	DateCreated  string                     `json:"date_created"`
	DateUpdated  string                     `json:"date_updated"`
	IsDeprecated bool                       `json:"is_deprecated"`
	Latest       experimentalPackageVersion `json:"latest"`
}

type experimentalPackageVersion struct {
	Namespace     string   `json:"namespace"`
	Name          string   `json:"name"`
	VersionNumber string   `json:"version_number"`
	FullName      string   `json:"full_name"`
	Description   string   `json:"description"`
	Icon          string   `json:"icon"`
	Dependencies  []string `json:"dependencies"`
	DownloadURL   string   `json:"download_url"`
	Downloads     int      `json:"downloads"`
	WebsiteURL    string   `json:"website_url"`
	IsActive      bool     `json:"is_active"`
}

var versionLike = regexp.MustCompile(`^\d+(?:\.\d+)+(?:[-+].*)?$`)

func NewThunderstore(community string, demo bool, baseURL string) *Thunderstore {
	_ = baseURL
	if community == "" {
		community = "valheim"
	}
	return &Thunderstore{
		community: community,
		demo:      demo,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		known:       map[string]Package{},
		searchCache: map[string]cachedSearch{},
	}
}

func (t *Thunderstore) Search(ctx context.Context, query string, page, pageSize int) (SearchResult, error) {
	if t.demo {
		return searchDemo(query, page, pageSize), nil
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	cacheKey := fmt.Sprintf("%s|%d", strings.ToLower(strings.TrimSpace(query)), page)
	t.mu.Lock()
	if cached, ok := t.searchCache[cacheKey]; ok && time.Since(cached.at) < 5*time.Minute {
		t.mu.Unlock()
		return cached.result, nil
	}
	t.mu.Unlock()

	endpoint := fmt.Sprintf(
		"https://thunderstore.io/api/cyberstorm/listing/%s/?page=%d&q=%s",
		url.PathEscape(t.community), page, url.QueryEscape(strings.TrimSpace(query)),
	)
	var listing cyberstormListing
	if err := t.fetchJSON(ctx, endpoint, &listing); err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{
		Total:    listing.Count,
		Page:     page,
		PageSize: min(pageSize, len(listing.Results)),
		Packages: make([]Package, 0, len(listing.Results)),
	}
	for _, item := range listing.Results {
		pkg := Package{
			Key:         makeKey(item.Namespace, item.Name),
			Owner:       item.Namespace,
			Name:        item.Name,
			FullName:    item.Namespace + "-" + item.Name,
			Description: item.Description,
			Icon:        item.IconURL,
			Version:     "latest",
		}
		result.Packages = append(result.Packages, pkg)
		t.mu.Lock()
		t.known[pkg.Key] = pkg
		t.mu.Unlock()
	}
	if result.PageSize == 0 && len(result.Packages) > 0 {
		result.PageSize = len(result.Packages)
	}
	t.mu.Lock()
	t.searchCache[cacheKey] = cachedSearch{at: time.Now(), result: result}
	t.mu.Unlock()
	return result, nil
}

func (t *Thunderstore) FindPackage(ctx context.Context, key string) (Package, error) {
	if t.demo {
		return findDemoPackage(key)
	}
	key = strings.ToLower(strings.TrimSpace(key))
	t.mu.Lock()
	if pkg, ok := t.known[key]; ok && pkg.DownloadURL != "" {
		t.mu.Unlock()
		return pkg, nil
	}
	t.mu.Unlock()

	if owner, name, ok := splitFullName(key); ok {
		if pkg, err := t.fetchPackage(ctx, owner, name, ""); err == nil {
			return pkg, nil
		}
	}

	searchTerm := key
	if parts := strings.SplitN(key, "-", 2); len(parts) == 2 && parts[1] != "" {
		searchTerm = parts[1]
	}
	search, err := t.Search(ctx, searchTerm, 1, 20)
	if err != nil {
		return Package{}, err
	}
	var selected *Package
	for i := range search.Packages {
		pkg := search.Packages[i]
		if pkg.Key == key || strings.EqualFold(pkg.FullName, key) {
			selected = &pkg
			break
		}
	}
	if selected == nil {
		for i := range search.Packages {
			pkg := search.Packages[i]
			if strings.Contains(strings.ToLower(pkg.FullName), key) {
				selected = &pkg
				break
			}
		}
	}
	if selected == nil {
		return Package{}, errors.New("thunderstore package not found")
	}
	return t.fetchPackage(ctx, selected.Owner, selected.Name, "")
}

func (t *Thunderstore) ResolveWithDependencies(ctx context.Context, pkg Package) ([]Package, error) {
	if t.demo {
		return resolveDemoDependencies(pkg)
	}
	visited := map[string]bool{}
	result := []Package{}
	var resolve func(Package) error
	resolve = func(current Package) error {
		if visited[current.Key] {
			return nil
		}
		if current.DownloadURL == "" {
			var err error
			current, err = t.fetchPackage(ctx, current.Owner, current.Name, "")
			if err != nil {
				return err
			}
		}
		visited[current.Key] = true
		for _, dependency := range current.Dependencies {
			dep, err := t.resolveDependency(ctx, dependency)
			if err != nil {
				continue
			}
			if err := resolve(dep); err != nil {
				return err
			}
			result = append(result, dep)
		}
		result = append(result, current)
		return nil
	}
	if err := resolve(pkg); err != nil {
		return nil, err
	}
	unique := []Package{}
	seen := map[string]bool{}
	for _, item := range result {
		if !seen[item.Key] {
			unique = append(unique, item)
			seen[item.Key] = true
		}
	}
	return unique, nil
}

func (t *Thunderstore) resolveDependency(ctx context.Context, dependency string) (Package, error) {
	fullName, version := splitDependency(dependency)
	if fullName == "" {
		return Package{}, errors.New("invalid dependency")
	}
	owner, name, ok := splitFullName(fullName)
	if ok {
		if pkg, err := t.fetchPackage(ctx, owner, name, version); err == nil {
			return pkg, nil
		}
	}
	search, err := t.Search(ctx, dependency, 1, 20)
	if err != nil {
		return Package{}, err
	}
	for _, candidate := range search.Packages {
		if strings.EqualFold(candidate.FullName, fullName) ||
			strings.HasPrefix(strings.ToLower(dependency), strings.ToLower(candidate.FullName)+"-") {
			return t.fetchPackage(ctx, candidate.Owner, candidate.Name, version)
		}
	}
	return Package{}, fmt.Errorf("dependency not found: %s", dependency)
}

func (t *Thunderstore) fetchPackage(ctx context.Context, owner, name, version string) (Package, error) {
	if owner == "" || name == "" {
		return Package{}, errors.New("invalid package name")
	}
	endpoint := fmt.Sprintf(
		"https://thunderstore.io/api/experimental/package/%s/%s/",
		url.PathEscape(owner), url.PathEscape(name),
	)
	var detail experimentalPackage
	if err := t.fetchJSON(ctx, endpoint, &detail); err != nil {
		return Package{}, err
	}
	latest := detail.Latest
	if version != "" && version != latest.VersionNumber {
		var specific experimentalPackageVersion
		versionEndpoint := fmt.Sprintf(
			"https://thunderstore.io/api/experimental/package/%s/%s/%s/",
			url.PathEscape(owner), url.PathEscape(name), url.PathEscape(version),
		)
		if err := t.fetchJSON(ctx, versionEndpoint, &specific); err == nil && specific.VersionNumber != "" {
			latest = specific
		}
	}
	fullName := detail.FullName
	if fullName == "" {
		fullName = owner + "-" + name
	}
	pkg := Package{
		Key:          makeKey(owner, name),
		Owner:        owner,
		Name:         name,
		FullName:     fullName,
		UUID:         detail.FullName,
		Description:  latest.Description,
		Icon:         latest.Icon,
		Version:      latest.VersionNumber,
		DownloadURL:  latest.DownloadURL,
		Dependencies: latest.Dependencies,
		Versions: []PackageVersion{{
			Version:      latest.VersionNumber,
			DownloadURL:  latest.DownloadURL,
			Dependencies: latest.Dependencies,
			Description:  latest.Description,
		}},
	}
	if pkg.Version == "" {
		pkg.Version = "latest"
	}
	t.mu.Lock()
	t.known[pkg.Key] = pkg
	t.mu.Unlock()
	return pkg, nil
}

func (t *Thunderstore) Download(ctx context.Context, pkg Package) ([]byte, error) {
	if t.demo || strings.HasPrefix(pkg.DownloadURL, "demo://") {
		return []byte("valheim-panel-demo:" + pkg.FullName + "\n"), nil
	}
	if pkg.DownloadURL == "" {
		fetched, err := t.fetchPackage(ctx, pkg.Owner, pkg.Name, pkg.Version)
		if err != nil {
			return nil, err
		}
		pkg = fetched
	}
	downloadClient := &http.Client{Timeout: 5 * time.Minute}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, pkg.DownloadURL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("user-agent", "valheim-panel/1.0")
		response, err := downloadClient.Do(request)
		if err != nil {
			lastErr = err
		} else {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<20))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 && readErr == nil {
				return data, nil
			}
			if readErr != nil {
				lastErr = readErr
			} else {
				lastErr = fmt.Errorf("mod download failed (%d) for %s", response.StatusCode, pkg.FullName)
			}
			if response.StatusCode < 500 && response.StatusCode != 429 {
				break
			}
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return nil, lastErr
}

func (t *Thunderstore) fetchJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("user-agent", "valheim-panel/1.0")
	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("thunderstore request failed (%d): %s", response.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("thunderstore returned an empty response")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("thunderstore invalid JSON: %w", err)
	}
	return nil
}

func splitDependency(value string) (string, string) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "-")
	if len(parts) < 3 {
		return value, ""
	}
	last := parts[len(parts)-1]
	if versionLike.MatchString(last) {
		return strings.Join(parts[:len(parts)-1], "-"), last
	}
	return value, ""
}

func splitFullName(value string) (string, string, bool) {
	index := strings.Index(value, "-")
	if index <= 0 || index >= len(value)-1 {
		return "", "", false
	}
	return value[:index], value[index+1:], true
}

func searchDemo(query string, page, pageSize int) SearchResult {
	all := demoPackages()
	term := strings.ToLower(strings.TrimSpace(query))
	filtered := []Package{}
	for _, pkg := range all {
		if term == "" || strings.Contains(strings.ToLower(pkg.Name), term) ||
			strings.Contains(strings.ToLower(pkg.Owner), term) ||
			strings.Contains(strings.ToLower(pkg.Description), term) {
			filtered = append(filtered, pkg)
		}
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := min(offset+pageSize, len(filtered))
	return SearchResult{
		Total: len(filtered), Page: page, PageSize: pageSize, Packages: filtered[offset:end],
	}
}

func findDemoPackage(key string) (Package, error) {
	key = strings.ToLower(key)
	for _, pkg := range demoPackages() {
		if pkg.Key == key {
			return pkg, nil
		}
	}
	return Package{}, errors.New("thunderstore package not found")
}

func resolveDemoDependencies(pkg Package) ([]Package, error) {
	all := demoPackages()
	byKey := map[string]Package{}
	for _, item := range all {
		byKey[item.Key] = item
	}
	result := []Package{}
	visited := map[string]bool{}
	var walk func(Package)
	walk = func(current Package) {
		if visited[current.Key] {
			return
		}
		visited[current.Key] = true
		for _, dependency := range current.Dependencies {
			fullName, _ := splitDependency(dependency)
			owner, name, ok := splitFullName(fullName)
			if !ok {
				continue
			}
			dep, ok := byKey[makeKey(owner, name)]
			if !ok {
				continue
			}
			walk(dep)
			result = append(result, dep)
		}
		result = append(result, current)
	}
	walk(pkg)
	unique := []Package{}
	seen := map[string]bool{}
	for _, item := range result {
		if !seen[item.Key] {
			seen[item.Key] = true
			unique = append(unique, item)
		}
	}
	return unique, nil
}

func demoPackages() []Package {
	return []Package{
		{
			Key: "denikson-bepinexpack_valheim", Owner: "denikson", Name: "BepInExPack_Valheim",
			FullName: "denikson-BepInExPack_Valheim", Description: "BepInEx pack for Valheim dedicated servers and clients.",
			Version: "5.4.2202", DownloadURL: "demo://BepInExPack_Valheim.zip",
			Versions: []PackageVersion{{Version: "5.4.2202", DownloadURL: "demo://BepInExPack_Valheim.zip"}},
		},
		{
			Key: "valheimmodding-jotunn", Owner: "ValheimModding", Name: "Jotunn",
			FullName: "ValheimModding-Jotunn", Description: "Modding framework and shared library for Valheim.",
			Version: "2.25.0", DownloadURL: "demo://Jotunn.zip",
			Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"},
			Versions:     []PackageVersion{{Version: "2.25.0", DownloadURL: "demo://Jotunn.zip", Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"}}},
		},
		{
			Key: "valheimplus-valheimplus", Owner: "ValheimPlus", Name: "ValheimPlus",
			FullName: "ValheimPlus-ValheimPlus", Description: "Large server and client configuration mod with a dedicated server build.",
			Version: "0.9.9.12", DownloadURL: "demo://ValheimPlus.zip",
			Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"},
			Versions:     []PackageVersion{{Version: "0.9.9.12", DownloadURL: "demo://ValheimPlus.zip", Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"}}},
		},
		{
			Key: "advize-planteverything", Owner: "Advize", Name: "PlantEverything",
			FullName: "Advize-PlantEverything", Description: "Allows players and servers to plant additional vegetation.",
			Version: "1.19.2", DownloadURL: "demo://PlantEverything.zip",
			Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"},
			Versions:     []PackageVersion{{Version: "1.19.2", DownloadURL: "demo://PlantEverything.zip", Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"}}},
		},
		{
			Key: "blacks7ar-serversync", Owner: "blacks7ar", Name: "ServerSync",
			FullName: "blacks7ar-ServerSync", Description: "Configuration synchronization helper used by many Valheim mods.",
			Version: "1.0.7", DownloadURL: "demo://ServerSync.zip",
			Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"},
			Versions:     []PackageVersion{{Version: "1.0.7", DownloadURL: "demo://ServerSync.zip", Dependencies: []string{"denikson-BepInExPack_Valheim-5.4.2202"}}},
		},
	}
}

func makeKey(parts ...string) string {
	value := strings.Join(parts, "-")
	value = strings.ToLower(value)
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		ok := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-'
		if ok {
			if char == '-' && lastDash {
				continue
			}
			builder.WriteRune(char)
			lastDash = char == '-'
		} else {
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func sortPackages(packages []Package) {
	sort.Slice(packages, func(i, j int) bool {
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
}
