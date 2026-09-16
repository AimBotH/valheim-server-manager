package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-panel/internal/auth"
	"valheim-panel/internal/panel"
	"valheim-panel/internal/store"
)

type Server struct {
	store   *store.Store
	manager *panel.Manager
	secret  string
	web     fs.FS
}

func New(store *store.Store, manager *panel.Manager, secret string, webFS fs.FS) *Server {
	return &Server{store: store, manager: manager, secret: secret, web: webFS}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.serveAPI(w, r)
		return
	}
	s.serveStatic(w, r)
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	pathValue := strings.TrimPrefix(r.URL.Path, "/api/")
	segments := splitPath(pathValue)
	if len(segments) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "API not found"})
		return
	}
	if segments[0] == "auth" {
		s.handleAuth(w, r, segments[1:])
		return
	}
	user, err := s.authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}

	switch segments[0] {
	case "overview":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		data, err := s.manager.Overview()
		if err != nil {
			writeError(w, err)
			return
		}
		result := struct {
			panel.Overview
			Settings store.Settings `json:"settings"`
		}{Overview: data, Settings: publicSettings(s.store.Snapshot().Settings)}
		writeJSON(w, http.StatusOK, result)
	case "system":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, http.StatusOK, s.manager.SystemInfo())
	case "settings":
		s.handleSettings(w, r)
	case "tasks":
		s.handleTasks(w, r, segments[1:])
	case "platform":
		s.handlePlatform(w, r, segments[1:])
	case "mods":
		s.handleModSearch(w, r, segments[1:])
	case "instances":
		s.handleInstances(w, r, segments[1:], user)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "API not found"})
	}
}

func (s *Server) handlePlatform(w http.ResponseWriter, r *http.Request, segments []string) {
	if len(segments) >= 2 && segments[0] == "steamcmd" && segments[1] == "install" && r.Method == http.MethodPost {
		task, err := s.manager.InstallSteamcmd()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, task)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "platform route not found"})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request, segments []string) {
	if len(segments) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "auth route not found"})
		return
	}
	switch segments[0] {
	case "login":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		user, ok := s.store.UserByUsername(body.Username)
		if !ok || !auth.VerifyPassword(body.Password, user.Password) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "用户名或密码错误"})
			return
		}
		token, err := auth.CreateToken(s.secret, user, 7*24*time.Hour)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token":    token,
			"user":     publicUser(user),
			"settings": publicSettings(s.store.Snapshot().Settings),
		})
	case "me":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		user, err := s.authenticate(r)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(user)})
	case "logout":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "password":
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		user, err := s.authenticate(r)
		if err != nil {
			writeError(w, err)
			return
		}
		var body struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		if !auth.VerifyPassword(body.CurrentPassword, user.Password) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "当前密码错误"})
			return
		}
		if len(body.NewPassword) < 8 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "新密码至少 8 个字符"})
			return
		}
		record, err := auth.HashPassword(body.NewPassword)
		if err != nil {
			writeError(w, err)
			return
		}
		err = s.store.Mutate(func(draft *store.State) error {
			for i := range draft.Users {
				if draft.Users[i].ID == user.ID {
					draft.Users[i].Password = record
					draft.Users[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				}
			}
			return nil
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "auth route not found"})
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, publicSettings(s.store.Snapshot().Settings))
		return
	}
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	err := s.store.Mutate(func(draft *store.State) error {
		if value := stringValue(body, "panelTitle"); value != "" {
			draft.Settings.PanelTitle = value
		}
		if value := stringValue(body, "publicUrl"); value != "" {
			draft.Settings.PublicURL = value
		}
		if value := stringValue(body, "steamcmdPath"); value != "" {
			draft.Settings.SteamcmdPath = value
		}
		if value := stringValue(body, "steamAppId"); value != "" {
			draft.Settings.SteamAppID = value
		}
		if value := stringValue(body, "installRoot"); value != "" {
			draft.Settings.InstallRoot = value
		}
		if value := stringValue(body, "thunderstoreCommunity"); value != "" {
			draft.Settings.ThunderstoreCommunity = value
		}
		if value, ok := body["defaultPort"]; ok {
			draft.Settings.DefaultPort = intValue(value, draft.Settings.DefaultPort)
		}
		if value, ok := body["defaultMaxPlayers"]; ok {
			draft.Settings.DefaultMaxPlayers = intValue(value, draft.Settings.DefaultMaxPlayers)
		}
		if value, ok := body["autoInstallBepInEx"]; ok {
			draft.Settings.AutoInstallBepInEx = boolValue(value, draft.Settings.AutoInstallBepInEx)
		}
		if value, ok := body["autoUpdateMods"]; ok {
			draft.Settings.AutoUpdateMods = boolValue(value, draft.Settings.AutoUpdateMods)
		}
		if value, ok := body["backupBeforeUpdate"]; ok {
			draft.Settings.BackupBeforeUpdate = boolValue(value, draft.Settings.BackupBeforeUpdate)
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicSettings(s.store.Snapshot().Settings))
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request, segments []string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if len(segments) == 0 {
		writeJSON(w, http.StatusOK, s.manager.Tasks(r.URL.Query().Get("instanceId")))
		return
	}
	task, ok := s.manager.Task(segments[0])
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "任务不存在"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleModSearch(w http.ResponseWriter, r *http.Request, segments []string) {
	if len(segments) > 0 && segments[0] == "recommended" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"packages": s.manager.RecommendedMods(r.Context(), 8)})
		return
	}
	if len(segments) == 0 || segments[0] != "search" || r.Method != http.MethodGet {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mod route not found"})
		return
	}
	query := r.URL.Query().Get("q")
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "pageSize", 24)
	result, err := s.manager.SearchMods(r.Context(), query, page, pageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request, segments []string, _ store.User) {
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			result, err := s.manager.List()
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodPost:
			body, err := mapBody(r)
			if err != nil {
				writeError(w, err)
				return
			}
			result, err := s.manager.Create(body)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, result)
		default:
			methodNotAllowed(w)
		}
		return
	}
	id := segments[0]
	rest := segments[1:]
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			result, err := s.manager.Get(id)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodPut:
			body, err := mapBody(r)
			if err != nil {
				writeError(w, err)
				return
			}
			result, err := s.manager.Update(id, body)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodDelete:
			purge := r.URL.Query().Get("purge") == "1"
			if err := s.manager.Delete(id, purge); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			methodNotAllowed(w)
		}
		return
	}
	switch rest[0] {
	case "install":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		task, err := s.manager.InstallServer(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, task)
	case "action":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		body, err := mapBody(r)
		if err != nil {
			writeError(w, err)
			return
		}
		action := stringValue(body, "action")
		var result any
		switch action {
		case "start":
			result, err = s.manager.Start(id)
		case "stop":
			result, err = s.manager.Stop(id)
		case "restart":
			result, err = s.manager.Restart(id)
		case "update":
			result, err = s.manager.InstallServer(id)
		default:
			err = &panel.HTTPError{Status: http.StatusBadRequest, Message: "未知操作"}
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "bepinex":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		task, err := s.manager.InstallBepInEx(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, task)
	case "logs":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		result, err := s.manager.Logs(id, queryInt(r, "lines", 240))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "config":
		if r.Method == http.MethodGet {
			result, err := s.manager.Config(id)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		if r.Method == http.MethodPut {
			body, err := mapBody(r)
			if err != nil {
				writeError(w, err)
				return
			}
			result, err := s.manager.UpdateConfig(id, body)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		methodNotAllowed(w)
	case "mods":
		s.handleMods(w, r, id, rest[1:])
	case "backups":
		s.handleBackups(w, r, id, rest[1:])
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "instance route not found"})
	}
}

func (s *Server) handleMods(w http.ResponseWriter, r *http.Request, id string, segments []string) {
	if len(segments) == 0 && r.Method == http.MethodGet {
		result, err := s.manager.ListMods(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(segments) == 0 {
		methodNotAllowed(w)
		return
	}
	switch segments[0] {
	case "install":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		body, err := mapBody(r)
		if err != nil {
			writeError(w, err)
			return
		}
		task, err := s.manager.InstallMod(id, stringValue(body, "key"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, task)
	case "upload":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		data, err := readLimited(r.Body, 256<<20)
		if err != nil {
			writeError(w, err)
			return
		}
		task, err := s.manager.UploadMod(id, r.URL.Query().Get("filename"), data)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, task)
	default:
		key, err := url.PathUnescape(segments[0])
		if err != nil {
			writeError(w, err)
			return
		}
		if len(segments) == 1 && r.Method == http.MethodDelete {
			if err := s.manager.RemoveMod(id, key); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if len(segments) < 2 {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mod route not found"})
			return
		}
		switch segments[1] {
		case "toggle":
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			body, err := mapBody(r)
			if err != nil {
				writeError(w, err)
				return
			}
			result, err := s.manager.ToggleMod(id, key, boolValue(body["enabled"], true))
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case "config":
			if r.Method == http.MethodGet {
				result, err := s.manager.ModConfig(id, key)
				if err != nil {
					writeError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, result)
				return
			}
			if r.Method == http.MethodPut {
				body, err := mapBody(r)
				if err != nil {
					writeError(w, err)
					return
				}
				if err := s.manager.UpdateModConfig(id, key, stringValue(body, "path"), rawStringValue(body, "content")); err != nil {
					writeError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			methodNotAllowed(w)
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mod route not found"})
		}
	}
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request, id string, segments []string) {
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			result, err := s.manager.Backups(id)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case http.MethodPost:
			body, err := mapBody(r)
			if err != nil {
				writeError(w, err)
				return
			}
			result, err := s.manager.CreateBackup(id, stringValue(body, "label"))
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, result)
		default:
			methodNotAllowed(w)
		}
		return
	}
	name, err := url.PathUnescape(segments[0])
	if err != nil {
		writeError(w, err)
		return
	}
	if len(segments) == 1 && r.Method == http.MethodDelete {
		if err := s.manager.DeleteBackup(id, name); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if len(segments) >= 2 && segments[1] == "restore" && r.Method == http.MethodPost {
		if err := s.manager.RestoreBackup(id, name); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if len(segments) >= 2 && segments[1] == "download" && r.Method == http.MethodGet {
		file, err := s.manager.BackupPath(id, name)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(file)+`"`)
		http.ServeFile(w, r, file)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "backup route not found"})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if cleanPath == "." || cleanPath == "" {
		cleanPath = "index.html"
	}
	data, err := fs.ReadFile(s.web, cleanPath)
	if err != nil {
		data, err = fs.ReadFile(s.web, "index.html")
		cleanPath = "index.html"
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if strings.HasSuffix(cleanPath, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}
	if strings.HasSuffix(cleanPath, ".css") {
		contentType = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) authenticate(r *http.Request) (store.User, error) {
	token := auth.TokenFromRequest(r)
	claims, err := auth.VerifyToken(s.secret, token)
	if err != nil {
		return store.User{}, &panel.HTTPError{Status: http.StatusUnauthorized, Message: "登录已失效，请重新登录"}
	}
	user, ok := s.store.UserByID(claims.Subject)
	if !ok {
		return store.User{}, &panel.HTTPError{Status: http.StatusUnauthorized, Message: "用户不存在"}
	}
	return user, nil
}

func decodeJSON(r *http.Request, target any) error {
	reader := io.LimitReader(r.Body, 4<<20)
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return &panel.HTTPError{Status: http.StatusBadRequest, Message: "请求参数错误"}
	}
	return nil
}

func mapBody(r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &panel.HTTPError{Status: http.StatusRequestEntityTooLarge, Message: "请求体过大"}
	}
	return data, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var httpErr *panel.HTTPError
	if errors.As(err, &httpErr) {
		status = httpErr.Status
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}

func splitPath(value string) []string {
	result := []string{}
	for _, part := range strings.Split(value, "/") {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func queryInt(r *http.Request, name string, fallback int) int {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func publicUser(user store.User) map[string]any {
	return map[string]any{
		"id": user.ID, "username": user.Username, "role": user.Role, "createdAt": user.CreatedAt,
	}
}

func publicSettings(settings store.Settings) store.Settings {
	settings.PanelSecret = ""
	return settings
}

func stringValue(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, ok := input[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func rawStringValue(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, ok := input[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	default:
		return fallback
	}
}

func ensureDefaultUser(stateStore *store.Store, username, password string) error {
	state := stateStore.Snapshot()
	if len(state.Users) > 0 {
		return nil
	}
	record, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return stateStore.Mutate(func(draft *store.State) error {
		draft.Users = append(draft.Users, store.User{
			ID: auth.ID("usr"), Username: username, Role: "admin",
			Password: record, CreatedAt: time.Now().UTC().Format(time.RFC3339),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	})
}

func NewServer(store *store.Store, manager *panel.Manager, secret string, webFS fs.FS, username, password string) (*Server, error) {
	if err := ensureDefaultUser(store, username, password); err != nil {
		return nil, err
	}
	return New(store, manager, secret, webFS), nil
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
