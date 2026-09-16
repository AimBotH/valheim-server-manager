package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"valheim-panel/internal/httpapi"
	"valheim-panel/internal/panel"
	"valheim-panel/internal/store"
)

//go:embed VERSION
var rawVersion string

var version = "v" + strings.TrimSpace(rawVersion) + "-go"

//go:embed web
var embeddedWeb embed.FS

func main() {
	var (
		bindPort = flag.Int("bind", envInt("PANEL_PORT", 8787), "HTTP listen port")
		dbPath   = flag.String("dbpath", env("PANEL_DATA_DIR", "data"), "data directory")
		level    = flag.String("level", env("PANEL_LEVEL", "info"), "log level")
		certFile = flag.String("cert", env("PANEL_CERT_FILE", ""), "TLS certificate file")
		keyFile  = flag.String("key", env("PANEL_KEY_FILE", ""), "TLS private key file")
		demo     = flag.Bool("demo", envBool("PANEL_DEMO", false), "enable demo mode")
		showVer  = flag.Bool("v", false, "print version")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("%s\n%s\n", version, runtime.Version())
		return
	}
	_ = level

	dataDir := *dbPath
	stateStore := store.New(dataDir)
	if err := stateStore.Load(); err != nil {
		log.Fatalf("load state: %v", err)
	}
	settings := stateStore.Snapshot().Settings
	secret := env("PANEL_SECRET", settings.PanelSecret)
	if secret == "" {
		secret = randomHex(32)
		if err := stateStore.Mutate(func(draft *store.State) error {
			draft.Settings.PanelSecret = secret
			return nil
		}); err != nil {
			log.Fatalf("save panel secret: %v", err)
		}
	}
	community := env("PANEL_THUNDERSTORE_COMMUNITY", settings.ThunderstoreCommunity)
	thunderBase := env("PANEL_THUNDERSTORE_BASE_URL", "")
	manager := panel.NewManager(stateStore, panel.NewThunderstore(community, *demo, thunderBase), dataDir, *demo)
	if err := manager.Init(); err != nil {
		log.Fatalf("init manager: %v", err)
	}

	webFS, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatalf("open embedded web: %v", err)
	}
	api, err := httpapi.NewServer(
		stateStore,
		manager,
		secret,
		webFS,
		env("PANEL_ADMIN_USERNAME", "admin"),
		env("PANEL_ADMIN_PASSWORD", "admin123"),
	)
	if err != nil {
		log.Fatalf("init API: %v", err)
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", *bindPort),
		Handler:           requestLogger(api.Handler()),
		ReadHeaderTimeout: 20 * time.Second,
	}
	go func() {
		var err error
		if *certFile != "" && *keyFile != "" {
			err = server.ListenAndServeTLS(*certFile, *keyFile)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	fmt.Printf("Valheim Panel %s listening on http://0.0.0.0:%d\n", version, *bindPort)
	fmt.Printf("Data directory: %s\n", dataDir)
	if *demo {
		fmt.Println("Demo mode enabled.")
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	stateStore.Close()
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
		return parsed
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
