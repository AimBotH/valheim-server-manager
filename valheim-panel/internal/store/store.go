package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type PasswordRecord struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

type User struct {
	ID        string         `json:"id"`
	Username  string         `json:"username"`
	Role      string         `json:"role"`
	Password  PasswordRecord `json:"password"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
}

type Settings struct {
	PanelTitle            string `json:"panelTitle"`
	PanelSecret           string `json:"panelSecret"`
	PublicURL             string `json:"publicUrl"`
	SteamcmdPath          string `json:"steamcmdPath"`
	SteamAppID            string `json:"steamAppId"`
	InstallRoot           string `json:"installRoot"`
	ThunderstoreCommunity string `json:"thunderstoreCommunity"`
	DefaultMaxPlayers     int    `json:"defaultMaxPlayers"`
	DefaultPort           int    `json:"defaultPort"`
	AutoInstallBepInEx    bool   `json:"autoInstallBepInEx"`
	AutoUpdateMods        bool   `json:"autoUpdateMods"`
	BackupBeforeUpdate    bool   `json:"backupBeforeUpdate"`
}

type Paths struct {
	RootDir         string `json:"rootDir"`
	InstallDir      string `json:"installDir"`
	SaveDir         string `json:"saveDir"`
	LogDir          string `json:"logDir"`
	BackupDir       string `json:"backupDir"`
	StagingDir      string `json:"stagingDir"`
	DisabledModsDir string `json:"disabledModsDir"`
}

type ServerConfig struct {
	Name         string `json:"name"`
	Port         int    `json:"port"`
	World        string `json:"world"`
	Password     string `json:"password"`
	Public       bool   `json:"public"`
	Crossplay    bool   `json:"crossplay"`
	SaveInterval int    `json:"saveInterval"`
	InstanceID   string `json:"instanceId"`
	ExtraArgs    string `json:"extraArgs"`
}

type SteamConfig struct {
	AppID       string `json:"appId"`
	Branch      string `json:"branch"`
	Installed   bool   `json:"installed"`
	InstalledAt string `json:"installedAt"`
	LastError   string `json:"lastError"`
}

type BepInExConfig struct {
	Enabled    bool   `json:"enabled"`
	Installed  bool   `json:"installed"`
	Version    string `json:"version"`
	PackageKey string `json:"packageKey"`
}

type ModRecord struct {
	Key          string   `json:"key"`
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	FullName     string   `json:"fullName"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Dependencies []string `json:"dependencies"`
	Enabled      bool     `json:"enabled"`
	InstalledAt  string   `json:"installedAt"`
	Files        []string `json:"files"`
	ConfigFiles  []string `json:"configFiles"`
}

type RuntimeState struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
	StoppedAt string `json:"stoppedAt"`
	ExitCode  int    `json:"exitCode"`
	LastError string `json:"lastError"`
}

type BackupConfig struct {
	Enabled       bool `json:"enabled"`
	IntervalHours int  `json:"intervalHours"`
	Keep          int  `json:"keep"`
}

type Instance struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	CreatedAt   string        `json:"createdAt"`
	UpdatedAt   string        `json:"updatedAt"`
	Paths       Paths         `json:"paths"`
	Server      ServerConfig  `json:"server"`
	Steam       SteamConfig   `json:"steam"`
	BepInEx     BepInExConfig `json:"bepinex"`
	Mods        []ModRecord   `json:"mods"`
	Runtime     RuntimeState  `json:"runtime"`
	Backup      BackupConfig  `json:"backup"`
}

type State struct {
	Version   int        `json:"version"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
	Users     []User     `json:"users"`
	Settings  Settings   `json:"settings"`
	Instances []Instance `json:"instances"`
}

type Store struct {
	mu     sync.RWMutex
	dir    string
	file   string
	state  State
	closed bool
}

func New(dir string) *Store {
	dir = filepath.Clean(dir)
	return &Store{
		dir:  dir,
		file: filepath.Join(dir, "panel.json"),
	}
}

func defaultState() State {
	now := time.Now().UTC().Format(time.RFC3339)
	return State{
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Users:     []User{},
		Settings: Settings{
			PanelTitle:            "Valheim 管理平台",
			SteamAppID:            "896660",
			ThunderstoreCommunity: "valheim",
			DefaultMaxPlayers:     10,
			DefaultPort:           2456,
			AutoInstallBepInEx:    true,
			AutoUpdateMods:        false,
			BackupBeforeUpdate:    true,
		},
		Instances: []Instance{},
	}
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(s.file)
	if errors.Is(err, os.ErrNotExist) {
		s.state = defaultState()
		return s.persistLocked()
	}
	if err != nil {
		return err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	base := defaultState()
	if state.Version == 0 {
		state.Version = base.Version
	}
	if state.CreatedAt == "" {
		state.CreatedAt = base.CreatedAt
	}
	if state.Settings.SteamAppID == "" {
		state.Settings.SteamAppID = base.Settings.SteamAppID
	}
	if state.Settings.ThunderstoreCommunity == "" {
		state.Settings.ThunderstoreCommunity = base.Settings.ThunderstoreCommunity
	}
	if state.Settings.DefaultPort == 0 {
		state.Settings.DefaultPort = base.Settings.DefaultPort
	}
	if state.Users == nil {
		state.Users = []User{}
	}
	if state.Instances == nil {
		state.Instances = []Instance{}
	}
	s.state = state
	return nil
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Store) Mutate(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	draft := cloneState(s.state)
	if err := fn(&draft); err != nil {
		return err
	}
	draft.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.state = draft
	return s.persistLocked()
}

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *Store) DataDir() string {
	return s.dir
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.dir, "panel-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, s.file)
}

func cloneState(state State) State {
	data, _ := json.Marshal(state)
	var clone State
	_ = json.Unmarshal(data, &clone)
	return clone
}

func (s *Store) UserByUsername(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	username = strings.ToLower(strings.TrimSpace(username))
	for _, user := range s.state.Users {
		if strings.ToLower(user.Username) == username {
			return user, true
		}
	}
	return User{}, false
}

func (s *Store) UserByID(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.state.Users {
		if user.ID == id {
			return user, true
		}
	}
	return User{}, false
}

func (s *Store) InstanceByID(id string) (Instance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, instance := range s.state.Instances {
		if instance.ID == id {
			return instance, true
		}
	}
	return Instance{}, false
}
