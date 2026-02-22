package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/initializ/forge/forge-core/a2a"
)

// Handler processes a JSON-RPC request and returns a response.
type Handler func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse

// SSEHandler streams SSE events for a JSON-RPC request.
type SSEHandler func(ctx context.Context, id any, rawParams json.RawMessage, w http.ResponseWriter, flusher http.Flusher)

// ServerConfig configures the A2A HTTP server.
type ServerConfig struct {
	Port      int
	AgentCard *a2a.AgentCard
}

// Server is an A2A-compliant HTTP server with JSON-RPC 2.0 dispatch.
type Server struct {
	port        int
	card        *a2a.AgentCard
	cardMu      sync.RWMutex
	store       *a2a.TaskStore
	handlers    map[string]Handler
	sseHandlers map[string]SSEHandler
	srv         *http.Server
}

// NewServer creates a new A2A server.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		port:        cfg.Port,
		card:        cfg.AgentCard,
		store:       a2a.NewTaskStore(),
		handlers:    make(map[string]Handler),
		sseHandlers: make(map[string]SSEHandler),
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

func (s *Server) agentCard() *a2a.AgentCard {
	s.cardMu.RLock()
	defer s.cardMu.RUnlock()
	return s.card
}

// Start begins serving HTTP. It blocks until the context is cancelled or
// an error occurs.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentCard)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /", s.handleJSONRPC)
	mux.HandleFunc("GET /", s.handleAgentCard)

	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: corsMiddleware(mux),
	}

	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.srv.Addr, err)
	}

	go func() {
		<-ctx.Done()
		s.srv.Shutdown(context.Background()) //nolint:errcheck
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

func init() {
	// Suppress default log timestamp for cleaner output
	log.SetFlags(0)
}
