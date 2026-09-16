package panel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"valheim-panel/internal/auth"
	"valheim-panel/internal/store"
)

const (
	defaultAppID   = "896660"
	defaultPort    = 2456
	bepInExPackage = "denikson-bepinexpack_valheim"
)

const steamcmdLinuxURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"
const steamcmdWindowsURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip"

type Manager struct {
	store     *store.Store
	thunder   *Thunderstore
	dataDir   string
	demo      bool
	mu        sync.Mutex
	processes map[string]*exec.Cmd
	stopping  map[string]bool
	demoRun   map[string]bool
	tasks     map[string]*Task
}

func NewManager(store *store.Store, thunder *Thunderstore, dataDir string, demo bool) *Manager {
	if absolute, err := filepath.Abs(dataDir); err == nil {
		dataDir = absolute
	}
	return &Manager{
		store:     store,
		thunder:   thunder,
		dataDir:   filepath.Clean(dataDir),
		demo:      demo,
		processes: map[string]*exec.Cmd{},
		stopping:  map[string]bool{},
		demoRun:   map[string]bool{},
		tasks:     map[string]*Task{},
	}
}

func (m *Manager) Init() error {
	state := m.store.Snapshot()
	for _, instance := range state.Instances {
		if err := m.ensureDirs(instance); err != nil {
			return err
		}
		if instance.Runtime.PID > 0 && !pidAlive(instance.Runtime.PID) {
			_ = m.store.Mutate(func(draft *store.State) error {
				for i := range draft.Instances {
					if draft.Instances[i].ID == instance.ID {
						draft.Instances[i].Runtime.PID = 0
						draft.Instances[i].Runtime.LastError = ""
					}
				}
				return nil
			})
		}
	}
	return nil
}

func (m *Manager) List() ([]InstanceView, error) {
	state := m.store.Snapshot()
	result := make([]InstanceView, 0, len(state.Instances))
	for _, instance := range state.Instances {
		status, err := m.Status(instance.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, InstanceView{Instance: instance, Status: status})
	}
	return result, nil
}

func (m *Manager) Get(id string) (InstanceView, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return InstanceView{}, httpError(http.StatusNotFound, "实例不存在")
	}
	status, err := m.Status(id)
	if err != nil {
		return InstanceView{}, err
	}
	return InstanceView{Instance: instance, Status: status}, nil
}

func (m *Manager) Create(input map[string]any) (InstanceView, error) {
	settings := m.store.Snapshot().Settings
	instance, err := makeInstance(input, settings, m.dataDir)
	if err != nil {
		return InstanceView{}, err
	}
	for _, existing := range m.store.Snapshot().Instances {
		if existing.ID == instance.ID || existing.Server.Port == instance.Server.Port {
			return InstanceView{}, httpError(http.StatusConflict, "实例 ID 或端口已存在")
		}
	}
	if err := m.ensureDirs(instance); err != nil {
		return InstanceView{}, err
	}
	if err := m.store.Mutate(func(draft *store.State) error {
		draft.Instances = append(draft.Instances, instance)
		return nil
	}); err != nil {
		return InstanceView{}, err
	}
	return m.Get(instance.ID)
}

func (m *Manager) Update(id string, input map[string]any) (InstanceView, error) {
	existing, ok := m.store.InstanceByID(id)
	if !ok {
		return InstanceView{}, httpError(http.StatusNotFound, "实例不存在")
	}
	normalized, err := normalizeInput(input, existing)
	if err != nil {
		return InstanceView{}, err
	}
	for _, other := range m.store.Snapshot().Instances {
		if other.ID != id && other.Server.Port == normalized.Server.Port {
			return InstanceView{}, httpError(http.StatusConflict, fmt.Sprintf("端口 %d 已被实例 %s 使用", normalized.Server.Port, other.Name))
		}
	}
	if err := m.store.Mutate(func(draft *store.State) error {
		for i := range draft.Instances {
			if draft.Instances[i].ID == id {
				draft.Instances[i].Name = normalized.Name
				draft.Instances[i].Description = normalized.Description
				draft.Instances[i].Server = normalized.Server
				draft.Instances[i].BepInEx = normalized.BepInEx
				draft.Instances[i].Backup = normalized.Backup
				draft.Instances[i].UpdatedAt = nowISO()
			}
		}
		return nil
	}); err != nil {
		return InstanceView{}, err
	}
	return m.Get(id)
}

func (m *Manager) Delete(id string, purge bool) error {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return httpError(http.StatusNotFound, "实例不存在")
	}
	_, _ = m.Stop(id)
	if purge {
		_ = os.RemoveAll(instance.Paths.RootDir)
		_ = os.RemoveAll(instance.Paths.InstallDir)
	}
	return m.store.Mutate(func(draft *store.State) error {
		filtered := draft.Instances[:0]
		for _, item := range draft.Instances {
			if item.ID != id {
				filtered = append(filtered, item)
			}
		}
		draft.Instances = filtered
		return nil
	})
}

func (m *Manager) Status(id string) (Status, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Status{}, httpError(http.StatusNotFound, "实例不存在")
	}
	m.mu.Lock()
	cmd := m.processes[id]
	demoRunning := m.demoRun[id]
	m.mu.Unlock()
	running := demoRunning
	pid := 0
	if cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
		running = pidAlive(pid)
	}
	if !running && instance.Runtime.PID > 0 && pidAlive(instance.Runtime.PID) {
		pid = instance.Runtime.PID
		running = true
	}
	installed := instance.Steam.Installed || fileExists(filepath.Join(instance.Paths.InstallDir, "valheim_server.x86_64"))
	diskBytes := dirSize(instance.Paths.RootDir)
	return Status{
		Running:          running,
		PID:              pid,
		Installed:        installed,
		InstallDir:       instance.Paths.InstallDir,
		SaveDir:          instance.Paths.SaveDir,
		LogDir:           instance.Paths.LogDir,
		DiskBytes:        diskBytes,
		Disk:             formatBytes(diskBytes),
		BepInExInstalled: instance.BepInEx.Installed || fileExists(filepath.Join(instance.Paths.InstallDir, "BepInEx")),
		Mods:             len(instance.Mods),
		EnabledMods:      enabledModCount(instance.Mods),
	}, nil
}

func (m *Manager) Task(id string) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return Task{}, false
	}
	return cloneTask(*task), true
}

func (m *Manager) Tasks(instanceID string) []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Task{}
	for _, task := range m.tasks {
		if instanceID == "" || task.InstanceID == instanceID {
			result = append(result, cloneTask(*task))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartedAt > result[j].StartedAt
	})
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}

func (m *Manager) runTask(kind, instanceID string, operation func(*Task, io.Writer) error) Task {
	task := &Task{
		ID:         auth.ID("task"),
		Type:       kind,
		InstanceID: instanceID,
		Status:     "running",
		StartedAt:  nowISO(),
		Lines:      []string{},
	}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()
	go func() {
		writer := taskLogWriter{task: task}
		err := operation(task, writer)
		m.mu.Lock()
		current := m.tasks[task.ID]
		current.FinishedAt = nowISO()
		if err != nil {
			current.Status = "failed"
			current.Error = err.Error()
			current.Lines = append(current.Lines, "ERROR: "+err.Error())
		} else {
			current.Status = "success"
		}
		m.mu.Unlock()
	}()
	return cloneTask(*task)
}

func (m *Manager) InstallServer(id string) (Task, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Task{}, httpError(http.StatusNotFound, "实例不存在")
	}
	return m.runTask("install", id, func(task *Task, writer io.Writer) error {
		if err := m.ensureDirs(instance); err != nil {
			return err
		}
		if m.demo {
			return m.demoInstall(instance, writer)
		}
		steamcmd, err := m.ResolveSteamcmd()
		if err != nil {
			return err
		}
		args := []string{"+force_install_dir", instance.Paths.InstallDir, "+login", "anonymous", "+app_update", instance.Steam.AppID}
		if instance.Steam.Branch != "" {
			args = append(args, "-beta", instance.Steam.Branch)
		}
		args = append(args, "validate", "+quit")
		logFile := filepath.Join(instance.Paths.LogDir, "steamcmd.log")
		file, _ := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if file != nil {
			defer file.Close()
		}
		output := io.MultiWriter(writer)
		if file != nil {
			output = io.MultiWriter(writer, file)
		}
		command := exec.Command(steamcmd, args...)
		command.Dir = m.dataDir
		command.Stdout = output
		command.Stderr = output
		if err := command.Run(); err != nil {
			return err
		}
		return m.store.Mutate(func(draft *store.State) error {
			for i := range draft.Instances {
				if draft.Instances[i].ID == id {
					draft.Instances[i].Steam.Installed = true
					draft.Instances[i].Steam.InstalledAt = nowISO()
					draft.Instances[i].Steam.LastError = ""
				}
			}
			return nil
		})
	}), nil
}

func (m *Manager) InstallSteamcmd() (Task, error) {
	return m.runTask("steamcmd", "", func(task *Task, writer io.Writer) error {
		targetDir := m.steamcmdDir()
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return err
		}
		executable := filepath.Join(targetDir, "steamcmd.sh")
		downloadURL := steamcmdLinuxURL
		if runtime.GOOS == "windows" {
			executable = filepath.Join(targetDir, "steamcmd.exe")
			downloadURL = steamcmdWindowsURL
		}
		if m.demo {
			content := "#!/bin/sh\necho SteamCMD demo\n"
			if runtime.GOOS == "windows" {
				content = "@echo off\r\necho SteamCMD demo\r\n"
			}
			if err := os.WriteFile(executable, []byte(content), 0o755); err != nil {
				return err
			}
			_, _ = io.WriteString(writer, "演示模式：SteamCMD 占位程序已安装。\n")
		} else {
			_, _ = fmt.Fprintf(writer, "下载 SteamCMD：%s\n", downloadURL)
			request, err := http.NewRequestWithContext(task.Context(), http.MethodGet, downloadURL, nil)
			if err != nil {
				return err
			}
			request.Header.Set("user-agent", "valheim-panel/1.0")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				return fmt.Errorf("SteamCMD 下载失败：HTTP %d", response.StatusCode)
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, 128<<20))
			if err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				if _, err := extractZipBuffer(data, targetDir); err != nil {
					return err
				}
			} else if err := extractTarGz(data, targetDir); err != nil {
				return err
			}
			_ = os.Chmod(executable, 0o755)
			command := exec.Command(executable, "+quit")
			command.Dir = targetDir
			command.Stdout = writer
			command.Stderr = writer
			if err := command.Run(); err != nil {
				_, _ = fmt.Fprintf(writer, "SteamCMD 首次自更新返回 %v，正在重试初始化。\n", err)
				retry := exec.Command(executable, "+quit")
				retry.Dir = targetDir
				retry.Stdout = writer
				retry.Stderr = writer
				if retryErr := retry.Run(); retryErr != nil {
					return fmt.Errorf("SteamCMD 初始化失败（路径：%s）：%w", executable, retryErr)
				}
			}
		}
		return m.store.Mutate(func(draft *store.State) error {
			draft.Settings.SteamcmdPath = executable
			return nil
		})
	}), nil
}

func (m *Manager) demoInstall(instance store.Instance, writer io.Writer) error {
	if err := os.MkdirAll(instance.Paths.InstallDir, 0o755); err != nil {
		return err
	}
	serverFile := filepath.Join(instance.Paths.InstallDir, "valheim_server.x86_64")
	if err := os.WriteFile(serverFile, []byte("#!/bin/sh\necho Valheim demo server\n"), 0o755); err != nil {
		return err
	}
	script := filepath.Join(instance.Paths.InstallDir, "start_server_bepinex.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho BepInEx demo launcher\n"), 0o755); err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Join(instance.Paths.InstallDir, "BepInEx", "plugins"), 0o755)
	_ = os.MkdirAll(filepath.Join(instance.Paths.InstallDir, "BepInEx", "config"), 0o755)
	_, _ = io.WriteString(writer, "演示模式：已生成 Valheim 服务器目录和启动脚本。\n")
	return m.store.Mutate(func(draft *store.State) error {
		for i := range draft.Instances {
			if draft.Instances[i].ID == instance.ID {
				draft.Instances[i].Steam.Installed = true
				draft.Instances[i].Steam.InstalledAt = nowISO()
			}
		}
		return nil
	})
}

func (m *Manager) Start(id string) (Status, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Status{}, httpError(http.StatusNotFound, "实例不存在")
	}
	status, _ := m.Status(id)
	if status.Running {
		return status, nil
	}
	if err := m.ensureDirs(instance); err != nil {
		return Status{}, err
	}
	logFile := filepath.Join(instance.Paths.LogDir, "server.log")
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Status{}, err
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "\n[%s] starting %s on UDP %d\n", nowISO(), instance.Server.Name, instance.Server.Port)

	var command *exec.Cmd
	if m.demo {
		_, _ = fmt.Fprintf(file, "Valheim demo server started on port %d\n", instance.Server.Port)
		m.mu.Lock()
		m.demoRun[id] = true
		m.mu.Unlock()
		_ = m.store.Mutate(func(draft *store.State) error {
			for i := range draft.Instances {
				if draft.Instances[i].ID == id {
					draft.Instances[i].Runtime.PID = 0
					draft.Instances[i].Runtime.StartedAt = nowISO()
					draft.Instances[i].Runtime.StoppedAt = ""
				}
			}
			return nil
		})
		return m.Status(id)
	} else {
		binary := filepath.Join(instance.Paths.InstallDir, "valheim_server.x86_64")
		if instance.BepInEx.Enabled && fileExists(filepath.Join(instance.Paths.InstallDir, "start_server_bepinex.sh")) {
			binary = filepath.Join(instance.Paths.InstallDir, "start_server_bepinex.sh")
		}
		if !fileExists(binary) {
			return Status{}, httpError(http.StatusConflict, "服务器尚未安装，请先执行安装或更新")
		}
		command = exec.Command(binary, m.launchArgs(instance)...)
	}
	command.Dir = instance.Paths.InstallDir
	command.Stdout = file
	command.Stderr = file
	command.Env = append(os.Environ(),
		"LD_LIBRARY_PATH="+strings.Join([]string{filepath.Join(instance.Paths.InstallDir, "linux64"), instance.Paths.InstallDir}, ":"),
	)
	configureCommand(command)
	if err := command.Start(); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	m.processes[id] = command
	delete(m.stopping, id)
	m.mu.Unlock()
	_ = m.store.Mutate(func(draft *store.State) error {
		for i := range draft.Instances {
			if draft.Instances[i].ID == id {
				draft.Instances[i].Runtime.PID = command.Process.Pid
				draft.Instances[i].Runtime.StartedAt = nowISO()
				draft.Instances[i].Runtime.StoppedAt = ""
			}
		}
		return nil
	})
	go func() {
		_ = command.Wait()
		m.mu.Lock()
		stopping := m.stopping[id]
		delete(m.processes, id)
		m.mu.Unlock()
		if !stopping {
			_ = m.store.Mutate(func(draft *store.State) error {
				for i := range draft.Instances {
					if draft.Instances[i].ID == id {
						draft.Instances[i].Runtime.PID = 0
						draft.Instances[i].Runtime.StoppedAt = nowISO()
					}
				}
				return nil
			})
		}
	}()
	return m.Status(id)
}

func (m *Manager) Stop(id string) (Status, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Status{}, httpError(http.StatusNotFound, "实例不存在")
	}
	m.mu.Lock()
	command := m.processes[id]
	demoRunning := m.demoRun[id]
	delete(m.demoRun, id)
	m.stopping[id] = true
	m.mu.Unlock()
	if m.demo && demoRunning {
		_ = m.store.Mutate(func(draft *store.State) error {
			for i := range draft.Instances {
				if draft.Instances[i].ID == id {
					draft.Instances[i].Runtime.PID = 0
					draft.Instances[i].Runtime.StoppedAt = nowISO()
				}
			}
			return nil
		})
		return m.Status(id)
	}
	pid := 0
	if command != nil && command.Process != nil {
		pid = command.Process.Pid
	} else if instance.Runtime.PID > 0 && pidAlive(instance.Runtime.PID) {
		pid = instance.Runtime.PID
	}
	if pid > 0 {
		killProcessTree(pid, false)
		for i := 0; i < 40 && pidAlive(pid); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if pidAlive(pid) {
			killProcessTree(pid, true)
		}
	}
	m.mu.Lock()
	delete(m.processes, id)
	m.mu.Unlock()
	_ = m.store.Mutate(func(draft *store.State) error {
		for i := range draft.Instances {
			if draft.Instances[i].ID == id {
				draft.Instances[i].Runtime.PID = 0
				draft.Instances[i].Runtime.StoppedAt = nowISO()
			}
		}
		return nil
	})
	return m.Status(id)
}

func (m *Manager) Restart(id string) (Status, error) {
	if _, err := m.Stop(id); err != nil {
		return Status{}, err
	}
	return m.Start(id)
}

func (m *Manager) Logs(id string, lines int) (LogView, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return LogView{}, httpError(http.StatusNotFound, "实例不存在")
	}
	if lines < 20 {
		lines = 20
	}
	if lines > 2000 {
		lines = 2000
	}
	return LogView{
		Server: tailFile(filepath.Join(instance.Paths.LogDir, "server.log"), lines),
		Steam:  tailFile(filepath.Join(instance.Paths.LogDir, "steamcmd.log"), lines/2),
	}, nil
}

func (m *Manager) Config(id string) (ConfigView, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return ConfigView{}, httpError(http.StatusNotFound, "实例不存在")
	}
	return ConfigView{
		Instance:   instance.Server,
		BepInEx:    instance.BepInEx,
		Backup:     instance.Backup,
		LaunchArgs: strings.Join(m.launchArgs(instance), " "),
	}, nil
}

func (m *Manager) UpdateConfig(id string, input map[string]any) (ConfigView, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return ConfigView{}, httpError(http.StatusNotFound, "实例不存在")
	}
	normalized, err := normalizeInput(input, instance)
	if err != nil {
		return ConfigView{}, err
	}
	err = m.store.Mutate(func(draft *store.State) error {
		for i := range draft.Instances {
			if draft.Instances[i].ID == id {
				draft.Instances[i].Name = normalized.Name
				draft.Instances[i].Description = normalized.Description
				draft.Instances[i].Server = normalized.Server
				draft.Instances[i].BepInEx = normalized.BepInEx
				draft.Instances[i].Backup = normalized.Backup
				draft.Instances[i].UpdatedAt = nowISO()
			}
		}
		return nil
	})
	if err != nil {
		return ConfigView{}, err
	}
	_ = os.WriteFile(filepath.Join(instance.Paths.RootDir, "launch-args.txt"), []byte(strings.Join(m.launchArgs(normalized), " ")+"\n"), 0o644)
	return m.Config(id)
}

func (m *Manager) InstallBepInEx(id string) (Task, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Task{}, httpError(http.StatusNotFound, "实例不存在")
	}
	return m.runTask("bepinex", id, func(task *Task, writer io.Writer) error {
		if m.demo {
			_ = os.MkdirAll(filepath.Join(instance.Paths.InstallDir, "BepInEx", "plugins"), 0o755)
			_ = os.MkdirAll(filepath.Join(instance.Paths.InstallDir, "BepInEx", "config"), 0o755)
			_ = os.WriteFile(filepath.Join(instance.Paths.InstallDir, "start_server_bepinex.sh"), []byte("#!/bin/sh\necho BepInEx demo launcher\n"), 0o755)
			_, _ = io.WriteString(writer, "演示模式：BepInEx 已安装。\n")
		} else {
			pkg, err := m.thunder.FindPackage(task.Context(), bepInExPackage)
			if err != nil {
				return err
			}
			if _, err := m.installPackage(instance, pkg, writer); err != nil {
				return err
			}
		}
		return m.store.Mutate(func(draft *store.State) error {
			for i := range draft.Instances {
				if draft.Instances[i].ID == id {
					draft.Instances[i].BepInEx.Installed = true
					draft.Instances[i].BepInEx.Enabled = true
					draft.Instances[i].BepInEx.Version = "installed"
					draft.Instances[i].BepInEx.PackageKey = bepInExPackage
				}
			}
			return nil
		})
	}), nil
}

func (m *Manager) SearchMods(ctx context.Context, query string, page, pageSize int) (SearchResult, error) {
	return m.thunder.Search(ctx, query, page, pageSize)
}

func (m *Manager) InstallMod(id, key string) (Task, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Task{}, httpError(http.StatusNotFound, "实例不存在")
	}
	return m.runTask("mod-install", id, func(task *Task, writer io.Writer) error {
		pkg, err := m.thunder.FindPackage(task.Context(), key)
		if err != nil {
			return err
		}
		queue, err := m.thunder.ResolveWithDependencies(task.Context(), pkg)
		if err != nil {
			return err
		}
		if instance.BepInEx.Enabled && !instance.BepInEx.Installed && !m.demo {
			bep, err := m.thunder.FindPackage(task.Context(), bepInExPackage)
			if err != nil {
				return err
			}
			_ = prependUnique(&queue, bep)
		}
		for _, dependency := range queue {
			current, _ := m.store.InstanceByID(id)
			if hasEnabledMod(current.Mods, dependency.Key) {
				continue
			}
			_, _ = fmt.Fprintf(writer, "安装 %s %s\n", dependency.FullName, dependency.Version)
			files, err := m.installPackage(instance, dependency, writer)
			if err != nil {
				return err
			}
			if err := m.store.Mutate(func(draft *store.State) error {
				for i := range draft.Instances {
					if draft.Instances[i].ID != id {
						continue
					}
					record := store.ModRecord{
						Key: dependency.Key, Owner: dependency.Owner, Name: dependency.Name,
						FullName: dependency.FullName, Version: dependency.Version,
						Description: dependency.Description, Dependencies: dependency.Dependencies,
						Enabled: true, InstalledAt: nowISO(), Files: files,
						ConfigFiles: configFiles(files),
					}
					replaced := false
					for j := range draft.Instances[i].Mods {
						if draft.Instances[i].Mods[j].Key == dependency.Key {
							draft.Instances[i].Mods[j] = record
							replaced = true
						}
					}
					if !replaced {
						draft.Instances[i].Mods = append(draft.Instances[i].Mods, record)
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (m *Manager) ListMods(id string) ([]store.ModRecord, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return nil, httpError(http.StatusNotFound, "实例不存在")
	}
	return instance.Mods, nil
}

func (m *Manager) ToggleMod(id, key string, enabled bool) ([]store.ModRecord, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return nil, httpError(http.StatusNotFound, "实例不存在")
	}
	for _, mod := range instance.Mods {
		if mod.Key != key {
			continue
		}
		if mod.Enabled == enabled {
			return instance.Mods, nil
		}
		for _, file := range mod.Files {
			source := filepath.Join(instance.Paths.DisabledModsDir, key, filepath.FromSlash(file))
			target := filepath.Join(instance.Paths.InstallDir, filepath.FromSlash(file))
			if enabled {
				source, target = target, source
			}
			if strings.HasPrefix(file, "BepInEx/config/") && !enabled {
				continue
			}
			if !fileExists(source) {
				continue
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Rename(source, target)
		}
	}
	return m.ListMods(id)
}

func (m *Manager) RemoveMod(id, key string) error {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return httpError(http.StatusNotFound, "实例不存在")
	}
	for _, mod := range instance.Mods {
		if mod.Key != key {
			continue
		}
		for _, file := range mod.Files {
			if strings.HasPrefix(file, "BepInEx/core/") {
				continue
			}
			_ = os.Remove(filepath.Join(instance.Paths.InstallDir, filepath.FromSlash(file)))
		}
	}
	_ = os.RemoveAll(filepath.Join(instance.Paths.DisabledModsDir, key))
	return m.store.Mutate(func(draft *store.State) error {
		for i := range draft.Instances {
			if draft.Instances[i].ID == id {
				filtered := draft.Instances[i].Mods[:0]
				for _, mod := range draft.Instances[i].Mods {
					if mod.Key != key {
						filtered = append(filtered, mod)
					}
				}
				draft.Instances[i].Mods = filtered
			}
		}
		return nil
	})
}

func (m *Manager) ModConfig(id, key string) (ModConfigView, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return ModConfigView{}, httpError(http.StatusNotFound, "实例不存在")
	}
	result := ModConfigView{Key: key, Files: []ModConfigFile{}}
	for _, mod := range instance.Mods {
		if mod.Key != key {
			continue
		}
		for _, file := range mod.ConfigFiles {
			full := filepath.Join(instance.Paths.InstallDir, filepath.FromSlash(file))
			data, err := os.ReadFile(full)
			if err == nil {
				result.Files = append(result.Files, ModConfigFile{Path: file, Content: string(data)})
			}
		}
	}
	return result, nil
}

func (m *Manager) UpdateModConfig(id, key, file, content string) error {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return httpError(http.StatusNotFound, "实例不存在")
	}
	if !strings.HasPrefix(file, "BepInEx/config/") {
		return httpError(http.StatusBadRequest, "只能修改 BepInEx/config 下的配置文件")
	}
	full, err := safeJoin(instance.Paths.InstallDir, file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

func (m *Manager) UploadMod(id, filename string, data []byte) (Task, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return Task{}, httpError(http.StatusNotFound, "实例不存在")
	}
	return m.runTask("mod-upload", id, func(task *Task, writer io.Writer) error {
		name := sanitizeFileName(filename)
		key := "local-" + strings.TrimSuffix(strings.ToLower(name), ".zip")
		files, err := extractZipBuffer(data, instance.Paths.InstallDir)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(writer, "已上传 %s\n", name)
		return m.store.Mutate(func(draft *store.State) error {
			for i := range draft.Instances {
				if draft.Instances[i].ID == id {
					draft.Instances[i].Mods = append(draft.Instances[i].Mods, store.ModRecord{
						Key: key, Owner: "local", Name: name, FullName: name, Version: "local",
						Description: "手动上传", Enabled: true, InstalledAt: nowISO(),
						Files: files, ConfigFiles: configFiles(files),
					})
				}
			}
			return nil
		})
	}), nil
}

func (m *Manager) Backups(id string) ([]BackupItem, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return nil, httpError(http.StatusNotFound, "实例不存在")
	}
	if err := os.MkdirAll(instance.Paths.BackupDir, 0o755); err != nil {
		return nil, err
	}
	entries, _ := os.ReadDir(instance.Paths.BackupDir)
	result := []BackupItem{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || (!info.Mode().IsRegular() && !entry.IsDir()) {
			continue
		}
		kind := "directory"
		if info.Mode().IsRegular() {
			kind = "archive"
		}
		result = append(result, BackupItem{
			Name: entry.Name(), Type: kind, Size: info.Size(), SizeText: formatBytes(info.Size()),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result, nil
}

func (m *Manager) CreateBackup(id, label string) (BackupItem, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return BackupItem{}, httpError(http.StatusNotFound, "实例不存在")
	}
	if err := m.ensureDirs(instance); err != nil {
		return BackupItem{}, err
	}
	if label == "" {
		label = time.Now().UTC().Format("20060102-150405Z")
	}
	label = sanitizeFileName(label)
	staging := filepath.Join(instance.Paths.StagingDir, "backup-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	defer os.RemoveAll(staging)
	_ = copyTree(instance.Paths.SaveDir, filepath.Join(staging, "save"))
	_ = copyTree(filepath.Join(instance.Paths.InstallDir, "BepInEx", "config"), filepath.Join(staging, "BepInEx", "config"))
	archive := filepath.Join(instance.Paths.BackupDir, label+".tar.gz")
	command := exec.Command("tar", "-czf", archive, "-C", staging, ".")
	if err := command.Run(); err != nil {
		return BackupItem{}, err
	}
	_ = m.pruneBackups(instance)
	info, _ := os.Stat(archive)
	return BackupItem{
		Name: filepath.Base(archive), Type: "archive", Size: info.Size(), SizeText: formatBytes(info.Size()),
		CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func (m *Manager) RestoreBackup(id, name string) error {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return httpError(http.StatusNotFound, "实例不存在")
	}
	archive, err := m.backupPath(instance, name)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(instance.Paths.SaveDir)
	_ = os.MkdirAll(instance.Paths.SaveDir, 0o755)
	staging := filepath.Join(instance.Paths.StagingDir, "restore-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	defer os.RemoveAll(staging)
	if err := exec.Command("tar", "-xzf", archive, "-C", staging).Run(); err != nil {
		return err
	}
	_ = copyTree(filepath.Join(staging, "save"), instance.Paths.SaveDir)
	configSource := filepath.Join(staging, "BepInEx", "config")
	if fileExists(configSource) {
		_ = os.RemoveAll(filepath.Join(instance.Paths.InstallDir, "BepInEx", "config"))
		_ = copyTree(configSource, filepath.Join(instance.Paths.InstallDir, "BepInEx", "config"))
	}
	return nil
}

func (m *Manager) DeleteBackup(id, name string) error {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return httpError(http.StatusNotFound, "实例不存在")
	}
	path, err := m.backupPath(instance, name)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func (m *Manager) BackupPath(id, name string) (string, error) {
	instance, ok := m.store.InstanceByID(id)
	if !ok {
		return "", httpError(http.StatusNotFound, "实例不存在")
	}
	return m.backupPath(instance, name)
}

func (m *Manager) Overview() (Overview, error) {
	instances, err := m.List()
	if err != nil {
		return Overview{}, err
	}
	var running, installed int
	var disk int64
	for _, instance := range instances {
		if instance.Status.Running {
			running++
		}
		if instance.Status.Installed {
			installed++
		}
		disk += instance.Status.DiskBytes
	}
	return Overview{
		Instances: len(instances), Running: running, Installed: installed,
		DiskBytes: disk, Disk: formatBytes(disk), Steamcmd: m.steamcmdPath(),
		Platform: runtime.GOOS + " " + runtime.GOARCH, Node: "Go " + runtime.Version(), DataDir: m.dataDir,
	}, nil
}

func (m *Manager) SystemInfo() SystemInfo {
	wd, _ := os.Getwd()
	return SystemInfo{
		Platform: runtime.GOOS, Arch: runtime.GOARCH, Node: "Go " + runtime.Version(),
		DataDir: m.dataDir, Steamcmd: m.steamcmdPath(), Cwd: wd,
	}
}

func (m *Manager) ResolveSteamcmd() (string, error) {
	path := m.steamcmdPath()
	if path == "" {
		return "", errors.New("未找到 SteamCMD，请先在设置中填写路径或执行 run.sh 安装")
	}
	return path, nil
}

func (m *Manager) launchArgs(instance store.Instance) []string {
	args := []string{
		"-name", instance.Server.Name,
		"-port", strconv.Itoa(instance.Server.Port),
		"-world", instance.Server.World,
		"-password", instance.Server.Password,
		"-public", boolString(instance.Server.Public),
		"-savedir", instance.Paths.SaveDir,
		"-nographics",
		"-batchmode",
	}
	if instance.Server.Crossplay {
		args = append(args, "-crossplay")
	}
	if strings.TrimSpace(instance.Server.ExtraArgs) != "" {
		args = append(args, strings.Fields(instance.Server.ExtraArgs)...)
	}
	return args
}

func (m *Manager) installPackage(instance store.Instance, pkg Package, writer io.Writer) ([]string, error) {
	data, err := m.thunder.Download(context.Background(), pkg)
	if err != nil {
		return nil, err
	}
	staging := filepath.Join(instance.Paths.StagingDir, "mod-"+pkg.Key+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, err
	}
	if m.demo || strings.HasPrefix(pkg.DownloadURL, "demo://") {
		plugin := filepath.Join(staging, "BepInEx", "plugins", pkg.Name+".dll")
		_ = os.MkdirAll(filepath.Dir(plugin), 0o755)
		_ = os.WriteFile(plugin, []byte("demo mod "+pkg.FullName+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(staging, "manifest.json"), []byte(`{"name":"`+pkg.Name+`"}`), 0o644)
	} else if _, err := extractZipBuffer(data, staging); err != nil {
		return nil, err
	}
	root, err := findContentRoot(staging)
	if err != nil {
		return nil, err
	}
	files, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	if pkg.Key != bepInExPackage {
		for _, file := range files {
			if strings.HasPrefix(file, "BepInEx/core/") {
				return nil, errors.New("模组包含 BepInEx/core 文件，已拒绝写入")
			}
		}
	}
	if err := copyTree(root, instance.Paths.InstallDir); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(writer, "写入 %d 个文件\n", len(files))
	return files, nil
}

func (m *Manager) ensureDirs(instance store.Instance) error {
	for _, dir := range []string{
		instance.Paths.RootDir, instance.Paths.InstallDir, instance.Paths.SaveDir,
		instance.Paths.LogDir, instance.Paths.BackupDir, instance.Paths.StagingDir,
		instance.Paths.DisabledModsDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) steamcmdPath() string {
	settings := m.store.Snapshot().Settings
	if settings.SteamcmdPath != "" {
		if fileExists(settings.SteamcmdPath) {
			return settings.SteamcmdPath
		}
	}
	local := filepath.Join(m.steamcmdDir(), "steamcmd.sh")
	if runtime.GOOS == "windows" {
		local = filepath.Join(m.steamcmdDir(), "steamcmd.exe")
	}
	if fileExists(local) {
		return local
	}
	for _, name := range []string{"steamcmd", "steamcmd.sh"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func (m *Manager) steamcmdDir() string {
	preferred := filepath.Join(m.dataDir, "steamcmd")
	if isASCII(preferred) {
		return preferred
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && isASCII(cacheDir) {
		return filepath.Join(cacheDir, "valheim-panel", "steamcmd")
	}
	return preferred
}

func (m *Manager) backupPath(instance store.Instance, name string) (string, error) {
	name = sanitizeFileName(name)
	archive := filepath.Join(instance.Paths.BackupDir, strings.TrimSuffix(name, ".tar.gz")+".tar.gz")
	if fileExists(archive) {
		return archive, nil
	}
	directory := filepath.Join(instance.Paths.BackupDir, strings.TrimSuffix(name, ".tar.gz"))
	if fileExists(directory) {
		return directory, nil
	}
	return "", httpError(http.StatusNotFound, "备份不存在")
}

func (m *Manager) pruneBackups(instance store.Instance) error {
	backups, err := m.Backups(instance.ID)
	if err != nil {
		return err
	}
	keep := instance.Backup.Keep
	if keep < 1 {
		keep = 7
	}
	for _, item := range backups[min(keep, len(backups)):] {
		_ = m.DeleteBackup(instance.ID, item.Name)
	}
	return nil
}

func makeInstance(input map[string]any, settings store.Settings, dataDir string) (store.Instance, error) {
	name := stringValue(input, "name")
	if name == "" || len(name) > 64 {
		return store.Instance{}, httpError(http.StatusBadRequest, "服务器名称需要为 1-64 个字符")
	}
	id := makeKey(name)
	root := filepath.Join(dataDir, "instances", id)
	if settings.InstallRoot != "" {
		root = filepath.Join(settings.InstallRoot, id)
	}
	installDir := filepath.Join(root, "server")
	instance := store.Instance{
		ID: id, Name: name, Description: stringValue(input, "description"),
		CreatedAt: nowISO(), UpdatedAt: nowISO(),
		Paths: store.Paths{
			RootDir: root, InstallDir: installDir, SaveDir: filepath.Join(root, "data"),
			LogDir: filepath.Join(root, "logs"), BackupDir: filepath.Join(root, "backups"),
			StagingDir: filepath.Join(root, "staging"), DisabledModsDir: filepath.Join(root, "mods-disabled"),
		},
		Steam:   store.SteamConfig{AppID: defaultAppID},
		BepInEx: store.BepInExConfig{Enabled: settings.AutoInstallBepInEx, PackageKey: bepInExPackage},
		Mods:    []store.ModRecord{},
		Backup:  store.BackupConfig{Enabled: true, IntervalHours: 24, Keep: 7},
	}
	normalized, err := normalizeInput(input, instance)
	if err != nil {
		return store.Instance{}, err
	}
	instance.Server = normalized.Server
	instance.BepInEx = normalized.BepInEx
	instance.Backup = normalized.Backup
	instance.Description = normalized.Description
	return instance, nil
}

func normalizeInput(input map[string]any, existing store.Instance) (store.Instance, error) {
	result := existing
	if value := stringValue(input, "name"); value != "" {
		if len(value) > 64 {
			return result, httpError(http.StatusBadRequest, "实例名称不能超过 64 个字符")
		}
		result.Name = value
	}
	if _, ok := input["description"]; ok {
		result.Description = stringValue(input, "description")
	}
	server := mapValue(input, "server")
	if server == nil {
		server = input
	}
	if value := stringValue(server, "name"); value != "" {
		result.Server.Name = value
	}
	if result.Server.Name == "" {
		result.Server.Name = result.Name
	}
	if value := stringValue(server, "world"); value != "" {
		if !validWorld(value) {
			return result, httpError(http.StatusBadRequest, "世界名称只能包含字母、数字、点、下划线和连字符")
		}
		result.Server.World = value
	}
	if result.Server.World == "" {
		result.Server.World = "Dedicated"
	}
	result.Server.Port = intValue(server, "port", result.Server.Port)
	if result.Server.Port < 1024 || result.Server.Port > 65535 {
		return result, httpError(http.StatusBadRequest, "端口必须在 1024-65535 之间")
	}
	if value := stringValue(server, "password"); value != "" {
		if len(value) < 5 {
			return result, httpError(http.StatusBadRequest, "Valheim 服务器密码至少 5 个字符")
		}
		result.Server.Password = value
	}
	if _, ok := server["public"]; ok {
		result.Server.Public = boolValue(server, "public", result.Server.Public)
	}
	if _, ok := server["crossplay"]; ok {
		result.Server.Crossplay = boolValue(server, "crossplay", result.Server.Crossplay)
	}
	if value, ok := server["saveInterval"]; ok {
		result.Server.SaveInterval = intFrom(value, result.Server.SaveInterval)
	}
	result.Server.InstanceID = stringValue(server, "instanceId")
	result.Server.ExtraArgs = stringValue(server, "extraArgs")
	bep := mapValue(input, "bepinex")
	if bep != nil {
		result.BepInEx.Enabled = boolValue(bep, "enabled", result.BepInEx.Enabled)
	}
	backup := mapValue(input, "backup")
	if backup != nil {
		result.Backup.Enabled = boolValue(backup, "enabled", result.Backup.Enabled)
		if value, ok := backup["keep"]; ok {
			result.Backup.Keep = intFrom(value, result.Backup.Keep)
		}
		if value, ok := backup["intervalHours"]; ok {
			result.Backup.IntervalHours = intFrom(value, result.Backup.IntervalHours)
		}
	}
	return result, nil
}

func extractZipBuffer(data []byte, destination string) ([]string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if name == "" || strings.Contains(name, "\x00") {
			continue
		}
		target, err := safeJoin(destination, name)
		if err != nil {
			continue
		}
		if file.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		source, err := file.Open()
		if err != nil {
			return nil, err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			source.Close()
			return nil, err
		}
		_, copyErr := io.Copy(output, io.LimitReader(source, 256<<20))
		source.Close()
		output.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		files = append(files, name)
	}
	return files, nil
}

func extractTarGz(data []byte, destination string) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destination, filepath.ToSlash(header.Name))
		if err != nil {
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, io.LimitReader(archive, 128<<20))
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
}

func findContentRoot(root string) (string, error) {
	if fileExists(filepath.Join(root, "BepInEx")) || fileExists(filepath.Join(root, "plugins")) ||
		fileExists(filepath.Join(root, "start_server_bepinex.sh")) {
		return root, nil
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found != "" || !entry.IsDir() || path == root {
			return nil
		}
		if fileExists(filepath.Join(path, "BepInEx")) || fileExists(filepath.Join(path, "plugins")) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	return root, nil
}

func copyTree(source, destination string) error {
	if !fileExists(source) {
		return nil
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func listFiles(root string) ([]string, error) {
	result := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative))
		return nil
	})
	return result, err
}

func configFiles(files []string) []string {
	result := []string{}
	for _, file := range files {
		lower := strings.ToLower(file)
		if strings.HasPrefix(file, "BepInEx/config/") &&
			(strings.HasSuffix(lower, ".cfg") || strings.HasSuffix(lower, ".json") ||
				strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) {
			result = append(result, file)
		}
	}
	return result
}

func safeJoin(root string, parts ...string) (string, error) {
	target := filepath.Join(append([]string{root}, parts...)...)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes root")
	}
	return target, nil
}

func tailFile(path string, lines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func formatBytes(bytes int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(bytes)
	index := 0
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%.0f %s", value, units[index])
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sanitizeFileName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "._")
	if result == "" {
		return "file"
	}
	return result
}

func validWorld(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func enabledModCount(mods []store.ModRecord) int {
	count := 0
	for _, mod := range mods {
		if mod.Enabled {
			count++
		}
	}
	return count
}

func hasEnabledMod(mods []store.ModRecord, key string) bool {
	for _, mod := range mods {
		if mod.Key == key && mod.Enabled {
			return true
		}
	}
	return false
}

func prependUnique(queue *[]Package, pkg Package) bool {
	for _, item := range *queue {
		if item.Key == pkg.Key {
			return false
		}
	}
	*queue = append([]Package{pkg}, *queue...)
	return true
}

func cloneTask(task Task) Task {
	task.Lines = append([]string(nil), task.Lines...)
	return task
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

func mapValue(input map[string]any, key string) map[string]any {
	if input == nil {
		return nil
	}
	value, ok := input[key]
	if !ok {
		return nil
	}
	result, _ := value.(map[string]any)
	return result
}

func boolValue(input map[string]any, key string, fallback bool) bool {
	value, ok := input[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	default:
		return fallback
	}
}

func intValue(input map[string]any, key string, fallback int) int {
	if input == nil {
		return fallback
	}
	value, ok := input[key]
	if !ok {
		return fallback
	}
	return intFrom(value, fallback)
}

func intFrom(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func isASCII(value string) bool {
	for _, char := range value {
		if char > 127 {
			return false
		}
	}
	return true
}

func httpError(status int, message string) error {
	return &HTTPError{Status: status, Message: message}
}

type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return e.Message
}

func (m *Manager) taskContext() context.Context {
	return context.Background()
}

func (t *Task) Context() context.Context {
	return context.Background()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
