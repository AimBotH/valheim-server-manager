package panel

import (
	"time"

	"valheim-panel/internal/store"
)

type Status struct {
	Running          bool   `json:"running"`
	PID              int    `json:"pid,omitempty"`
	Installed        bool   `json:"installed"`
	InstallDir       string `json:"installDir"`
	SaveDir          string `json:"saveDir"`
	LogDir           string `json:"logDir"`
	DiskBytes        int64  `json:"diskBytes"`
	Disk             string `json:"disk"`
	BepInExInstalled bool   `json:"bepinexInstalled"`
	Mods             int    `json:"mods"`
	EnabledMods      int    `json:"enabledMods"`
}

type InstanceView struct {
	store.Instance
	Status Status `json:"status"`
}

type Task struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	InstanceID string   `json:"instanceId"`
	Status     string   `json:"status"`
	StartedAt  string   `json:"startedAt"`
	FinishedAt string   `json:"finishedAt"`
	Error      string   `json:"error"`
	Lines      []string `json:"lines"`
}

type PackageVersion struct {
	Version      string   `json:"version"`
	DownloadURL  string   `json:"downloadUrl"`
	Dependencies []string `json:"dependencies"`
	Description  string   `json:"description"`
}

type Package struct {
	Key          string           `json:"key"`
	Owner        string           `json:"owner"`
	Name         string           `json:"name"`
	FullName     string           `json:"fullName"`
	UUID         string           `json:"uuid"`
	Description  string           `json:"description"`
	Icon         string           `json:"icon"`
	Version      string           `json:"version"`
	DownloadURL  string           `json:"downloadUrl"`
	Dependencies []string         `json:"dependencies"`
	Versions     []PackageVersion `json:"versions"`
}

type SearchResult struct {
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
	Packages []Package `json:"packages"`
}

type BackupItem struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	SizeText  string `json:"sizeText"`
	CreatedAt string `json:"createdAt"`
}

type Overview struct {
	Instances int    `json:"instances"`
	Running   int    `json:"running"`
	Installed int    `json:"installed"`
	DiskBytes int64  `json:"diskBytes"`
	Disk      string `json:"disk"`
	Steamcmd  string `json:"steamcmd"`
	Platform  string `json:"platform"`
	Node      string `json:"node"`
	DataDir   string `json:"dataDir"`
}

type SystemInfo struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Node     string `json:"node"`
	DataDir  string `json:"dataDir"`
	Steamcmd string `json:"steamcmd"`
	Cwd      string `json:"cwd"`
}

type ConfigView struct {
	Instance   store.ServerConfig  `json:"instance"`
	BepInEx    store.BepInExConfig `json:"bepinex"`
	Backup     store.BackupConfig  `json:"backup"`
	LaunchArgs string              `json:"launchArgs"`
}

type ModConfigView struct {
	Key   string          `json:"key"`
	Files []ModConfigFile `json:"files"`
}

type ModConfigFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type LogView struct {
	Server string `json:"server"`
	Steam  string `json:"steam"`
}

type processEntry struct {
	PID       int
	Stopping  bool
	StartedAt string
	Command   any
}

type taskLogWriter struct {
	task *Task
}

func (w taskLogWriter) Write(data []byte) (int, error) {
	if w.task != nil {
		lines := splitLines(string(data))
		for _, line := range lines {
			if line == "" {
				continue
			}
			w.task.Lines = append(w.task.Lines, line)
			if len(w.task.Lines) > 300 {
				w.task.Lines = w.task.Lines[len(w.task.Lines)-300:]
			}
		}
	}
	return len(data), nil
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func splitLines(value string) []string {
	result := []string{}
	start := 0
	for i, r := range value {
		if r == '\n' || r == '\r' {
			if i > start {
				result = append(result, value[start:i])
			}
			start = i + 1
		}
	}
	if start < len(value) {
		result = append(result, value[start:])
	}
	return result
}
