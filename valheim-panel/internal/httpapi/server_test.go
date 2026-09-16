package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"valheim-panel/internal/panel"
	"valheim-panel/internal/store"
)

func TestSmokeFlow(t *testing.T) {
	dataDir := t.TempDir()
	stateStore := store.New(dataDir)
	if err := stateStore.Load(); err != nil {
		t.Fatal(err)
	}
	manager := panel.NewManager(stateStore, panel.NewThunderstore("valheim", true, ""), dataDir, true)
	if err := manager.Init(); err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{
		"index.html": {Data: []byte("<html><title>Valheim Panel</title></html>")},
	}
	server, err := NewServer(stateStore, manager, "test-secret", web, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer stateStore.Close()

	status, _ := requestJSON(t, httpServer.URL, http.MethodGet, "/", nil, "")
	if status != http.StatusOK {
		t.Fatalf("page status = %d", status)
	}

	status, body := requestJSON(t, httpServer.URL, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin",
		"password": "test-password",
	}, "")
	if status != http.StatusOK {
		t.Fatalf("login status = %d body=%v", status, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("missing token")
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/platform/steamcmd/install", nil, token)
	if status != http.StatusAccepted {
		t.Fatalf("steamcmd install status = %d body=%v", status, body)
	}
	waitTask(t, httpServer.URL, token, body["id"].(string))
	status, body = requestJSON(t, httpServer.URL, http.MethodGet, "/api/system", nil, token)
	if status != http.StatusOK || body["steamcmd"] == "" {
		t.Fatalf("steamcmd system status = %d body=%v", status, body)
	}
	status, body = requestJSON(t, httpServer.URL, http.MethodGet, "/api/mods/recommended", nil, token)
	recommended, _ := body["packages"].([]any)
	if status != http.StatusOK || len(recommended) == 0 {
		t.Fatalf("recommended mods status = %d body=%v", status, body)
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances", map[string]any{
		"name":        "smoke-server",
		"description": "test",
		"server": map[string]any{
			"name": "Smoke Valheim", "world": "Dedicated", "port": 2456,
			"password": "secret", "public": true,
		},
	}, token)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%v", status, body)
	}
	instanceID, _ := body["id"].(string)
	if instanceID == "" {
		t.Fatal("missing instance id")
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances/"+instanceID+"/install", nil, token)
	if status != http.StatusAccepted {
		t.Fatalf("install status = %d body=%v", status, body)
	}
	taskID, _ := body["id"].(string)
	waitTask(t, httpServer.URL, token, taskID)

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances/"+instanceID+"/action", map[string]any{"action": "start"}, token)
	if status != http.StatusOK || body["running"] != true {
		t.Fatalf("start status = %d body=%v", status, body)
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodGet, "/api/mods/search?q=BepInEx&pageSize=10", nil, token)
	if status != http.StatusOK {
		t.Fatalf("search status = %d body=%v", status, body)
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances/"+instanceID+"/bepinex", nil, token)
	if status != http.StatusAccepted {
		t.Fatalf("bepinex status = %d body=%v", status, body)
	}
	waitTask(t, httpServer.URL, token, body["id"].(string))

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances/"+instanceID+"/mods/install", map[string]any{"key": "valheimmodding-jotunn"}, token)
	if status != http.StatusAccepted {
		t.Fatalf("mod install status = %d body=%v", status, body)
	}
	waitTask(t, httpServer.URL, token, body["id"].(string))

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances/"+instanceID+"/backups", map[string]any{"label": "smoke-backup"}, token)
	if status != http.StatusCreated {
		t.Fatalf("backup status = %d body=%v", status, body)
	}
	if !fileExists(filepath.Join(dataDir, "instances", instanceID, "backups", "smoke-backup.tar.gz")) {
		t.Fatal("backup archive not created")
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodPost, "/api/instances/"+instanceID+"/action", map[string]any{"action": "stop"}, token)
	if status != http.StatusOK || body["running"] != false {
		t.Fatalf("stop status = %d body=%v", status, body)
	}

	status, body = requestJSON(t, httpServer.URL, http.MethodPut, "/api/auth/password", map[string]any{
		"currentPassword": "test-password", "newPassword": "test-password-2",
	}, token)
	if status != http.StatusOK {
		t.Fatalf("password status = %d body=%v", status, body)
	}
	status, _ = requestJSON(t, httpServer.URL, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": "test-password-2",
	}, "")
	if status != http.StatusOK {
		t.Fatalf("relogin status = %d", status)
	}
}

func requestJSON(t *testing.T, base, method, path string, payload any, token string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if path == "/" {
		return response.StatusCode, map[string]any{"raw": string(data)}
	}
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	return response.StatusCode, body
}

func waitTask(t *testing.T, base, token, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		status, body := requestJSON(t, base, http.MethodGet, "/api/tasks/"+id, nil, token)
		if status != http.StatusOK {
			t.Fatalf("task status = %d body=%v", status, body)
		}
		if body["status"] != "running" {
			if body["status"] != "success" {
				t.Fatalf("task failed: %v", body)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("task timeout")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
