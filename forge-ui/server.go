package forgeui

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/initializ/forge/forge-ui/static"
)

// UIServerConfig configures the UI dashboard server.
type UIServerConfig struct {
	Port        int             // default: 4200
	WorkDir     string          // workspace root to scan for agents
	StartFunc   AgentStartFunc  // injected by forge-cli
	CreateFunc  AgentCreateFunc // injected by forge-cli (Phase 3)
	OAuthFunc   OAuthFlowFunc   // injected by forge-cli (optional, for OAuth login)
	AgentPort   int             // base port for agent allocation (default: 9100)
	OpenBrowser bool            // open browser on start
}

// UIServer serves the Forge dashboard UI and API.
type UIServer struct {
	cfg     UIServerConfig
	scanner *Scanner
	pm      *ProcessManager
	broker  *SSEBroker
	srv     *http.Server
}

// NewUIServer creates a UIServer with the given configuration.
func NewUIServer(cfg UIServerConfig) *UIServer {
	if cfg.Port == 0 {
		cfg.Port = 4200
	}
	if cfg.AgentPort == 0 {
		cfg.AgentPort = 9100
	}

	broker := NewSSEBroker()
	scanner := NewScanner(cfg.WorkDir)
	pm := NewProcessManager(cfg.StartFunc, broker, cfg.AgentPort)

	return &UIServer{
		cfg:     cfg,
		scanner: scanner,
		pm:      pm,
		broker:  broker,
	}
}

// Start starts the server and blocks until ctx is cancelled.
func (s *UIServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("POST /api/agents/rescan", s.handleRescan)
	mux.HandleFunc("GET /api/agents/{id}", s.handleGetAgent)
	mux.HandleFunc("POST /api/agents/{id}/start", s.handleStartAgent)
	mux.HandleFunc("POST /api/agents/{id}/stop", s.handleStopAgent)
	mux.HandleFunc("POST /api/agents/{id}/chat", s.handleChat)
	mux.HandleFunc("GET /api/agents/{id}/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/agents/{id}/sessions/{sid}", s.handleGetSession)

	// Phase 3: Create & Configure routes
	mux.HandleFunc("GET /api/wizard/meta", s.handleGetWizardMeta)
	mux.HandleFunc("POST /api/agents", s.handleCreateAgent)
	mux.HandleFunc("GET /api/agents/{id}/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/agents/{id}/config", s.handleUpdateConfig)
	mux.HandleFunc("POST /api/agents/{id}/config/validate", s.handleValidateConfig)
	mux.HandleFunc("GET /api/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/skills/{name}/content", s.handleGetSkillContent)
	mux.HandleFunc("GET /api/tools", s.handleListBuiltinTools)
	mux.HandleFunc("POST /api/oauth/start", s.handleOAuthStart)

	// Static file serving with SPA fallback
	distFS, err := fs.Sub(static.FS, "dist")
	if err != nil {
		return fmt.Errorf("creating sub filesystem: %w", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the file directly
		path := r.URL.Path
		if path == "/" {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Check if the file exists
		f, err := distFS.Open(strings.TrimPrefix(path, "/"))
		if err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for non-file paths
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
	s.srv = &http.Server{
		Addr:         addr,
		Handler:      corsMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // disabled for SSE
	}

	// Start listener
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("binding to %s: %w", addr, err)
	}

	fmt.Printf("\n  Forge Dashboard\n")
	fmt.Printf("  ─────────────────────────────────\n")
	fmt.Printf("  URL:       http://%s\n", addr)
	fmt.Printf("  Workspace: %s\n", s.cfg.WorkDir)
	fmt.Printf("  ─────────────────────────────────\n\n")

	if s.cfg.OpenBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			openBrowser(fmt.Sprintf("http://%s", addr))
		}()
	}

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		s.pm.StopAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	if err := s.srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// corsMiddleware adds CORS headers.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// openBrowser opens the default browser to the given URL.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return
	}
	_ = cmd.Start()
}
