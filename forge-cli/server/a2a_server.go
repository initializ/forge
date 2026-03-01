package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
)

// Handler processes a JSON-RPC request and returns a response.
type Handler func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse

// SSEHandler streams SSE events for a JSON-RPC request.
type SSEHandler func(ctx context.Context, id any, rawParams json.RawMessage, w http.ResponseWriter, flusher http.Flusher)

// ServerConfig configures the A2A HTTP server.
type ServerConfig struct {
	Port            int
	Host            string        // bind address (default "" = all interfaces)
	ShutdownTimeout time.Duration // graceful shutdown timeout (0 = immediate)
	AgentCard       *a2a.AgentCard
}

type httpRoute struct {
	pattern string
	handler http.HandlerFunc
}

// Server is an A2A-compliant HTTP server with JSON-RPC 2.0 dispatch.
type Server struct {
	port            int
	host            string
	shutdownTimeout time.Duration
	card            *a2a.AgentCard
	cardMu          sync.RWMutex
	store           *a2a.TaskStore
	handlers        map[string]Handler
	sseHandlers     map[string]SSEHandler
	httpHandlers    []httpRoute
	srv             *http.Server
}

// NewServer creates a new A2A server.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		port:            cfg.Port,
		host:            cfg.Host,
		shutdownTimeout: cfg.ShutdownTimeout,
		card:            cfg.AgentCard,
		store:           a2a.NewTaskStore(),
		handlers:        make(map[string]Handler),
		sseHandlers:     make(map[string]SSEHandler),
	}
	return s
}

// RegisterHandler registers a JSON-RPC method handler.
func (s *Server) RegisterHandler(method string, h Handler) {
	s.handlers[method] = h
}

// RegisterSSEHandler registers an SSE-streaming JSON-RPC method handler.
func (s *Server) RegisterSSEHandler(method string, h SSEHandler) {
	s.sseHandlers[method] = h
}

// RegisterHTTPHandler registers a standard HTTP handler on the server's mux.
// Used for REST-style endpoints alongside JSON-RPC.
func (s *Server) RegisterHTTPHandler(pattern string, handler http.HandlerFunc) {
	s.httpHandlers = append(s.httpHandlers, httpRoute{pattern, handler})
}

// UpdateAgentCard replaces the agent card (for hot-reload).
func (s *Server) UpdateAgentCard(card *a2a.AgentCard) {
	s.cardMu.Lock()
	defer s.cardMu.Unlock()
	s.card = card
}

// TaskStore returns the server's task store.
func (s *Server) TaskStore() *a2a.TaskStore {
	return s.store
}

// Port returns the port the server is configured to listen on (or the actual
// port after Start resolves port conflicts).
func (s *Server) Port() int {
	return s.port
}

func (s *Server) agentCard() *a2a.AgentCard {
	s.cardMu.RLock()
	defer s.cardMu.RUnlock()
	return s.card
}

// Start begins serving HTTP. It blocks until the context is cancelled or
// an error occurs.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Register REST-style HTTP handlers first (more specific patterns)
	for _, route := range s.httpHandlers {
		mux.HandleFunc(route.pattern, route.handler)
	}

	// Register core A2A handlers
	mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentCard)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /", s.handleJSONRPC)
	mux.HandleFunc("GET /", s.handleAgentCard)

	s.srv = &http.Server{
		Handler:      corsMiddleware(mux),
		WriteTimeout: 0, // SSE-safe: no write deadline
		IdleTimeout:  120 * time.Second,
	}

	// Try specified port, then auto-increment up to 10 times on conflict.
	var ln net.Listener
	var listenErr error
	actualPort := s.port
	for range 10 {
		addr := fmt.Sprintf("%s:%d", s.host, actualPort)
		ln, listenErr = net.Listen("tcp", addr)
		if listenErr == nil {
			break
		}
		if !isAddrInUse(listenErr) {
			return fmt.Errorf("listen on %s: %w", addr, listenErr)
		}
		actualPort++
	}
	if listenErr != nil {
		return fmt.Errorf("all ports %d-%d in use: %w", s.port, actualPort, listenErr)
	}
	s.port = actualPort // update so banner/info reflect actual port
	s.srv.Addr = fmt.Sprintf("%s:%d", s.host, actualPort)

	go func() {
		<-ctx.Done()
		shutdownCtx := context.Background()
		if s.shutdownTimeout > 0 {
			var cancel context.CancelFunc
			shutdownCtx, cancel = context.WithTimeout(shutdownCtx, s.shutdownTimeout)
			defer cancel()
		}
		s.srv.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
}

func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.agentCard()) //nolint:errcheck
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
}

func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	var req a2a.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, a2a.NewErrorResponse(nil, a2a.ErrCodeParseError, "parse error: "+err.Error()))
		return
	}

	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusOK, a2a.NewErrorResponse(req.ID, a2a.ErrCodeInvalidRequest, "jsonrpc must be \"2.0\""))
		return
	}

	// Check SSE handlers first (for streaming methods)
	if h, ok := s.sseHandlers[req.Method]; ok {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusOK, a2a.NewErrorResponse(req.ID, a2a.ErrCodeInternal, "streaming not supported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		h(r.Context(), req.ID, req.Params, w, flusher)
		return
	}

	// Check regular handlers
	if h, ok := s.handlers[req.Method]; ok {
		resp := h(r.Context(), req.ID, req.Params)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	writeJSON(w, http.StatusOK, a2a.NewErrorResponse(req.ID, a2a.ErrCodeMethodNotFound, "method not found: "+req.Method))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteSSEEvent writes a single SSE event to the response writer.
func WriteSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
	flusher.Flush()
	return nil
}

// isAddrInUse returns true if the error indicates the address is already in use.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr *syscall.Errno
		if errors.As(opErr.Err, &sysErr) {
			return *sysErr == syscall.EADDRINUSE
		}
	}
	return false
}

func init() {
	// Suppress default log timestamp for cleaner output
	log.SetFlags(0)
}
