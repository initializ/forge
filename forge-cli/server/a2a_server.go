package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"golang.org/x/time/rate"
)

// Handler processes a JSON-RPC request and returns a response.
type Handler func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse

// SSEHandler streams SSE events for a JSON-RPC request.
type SSEHandler func(ctx context.Context, id any, rawParams json.RawMessage, w http.ResponseWriter, flusher http.Flusher)

// RateLimitConfig controls per-IP rate limiting.
type RateLimitConfig struct {
	ReadRPS    float64 // requests per second for read operations (GET/HEAD/OPTIONS)
	ReadBurst  int     // burst size for reads
	WriteRPS   float64 // requests per second for write operations (POST/PUT/DELETE)
	WriteBurst int     // burst size for writes
}

// ServerConfig configures the A2A HTTP server.
type ServerConfig struct {
	Port            int
	Host            string        // bind address (default "" = all interfaces)
	ShutdownTimeout time.Duration // graceful shutdown timeout (0 = immediate)
	AgentCard       *a2a.AgentCard
	AuthMiddleware  func(http.Handler) http.Handler // optional auth middleware
	AllowedOrigins  []string                        // CORS allowed origins
	RateLimit       *RateLimitConfig                // optional rate limit config
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
	authMiddleware  func(http.Handler) http.Handler
	allowedOrigins  []string
	rateLimit       *RateLimitConfig
	srv             *http.Server
}

// NewServer creates a new A2A server.
func NewServer(cfg ServerConfig) *Server {
	allowedOrigins := cfg.AllowedOrigins
	if len(allowedOrigins) == 0 {
		allowedOrigins = DefaultAllowedOrigins()
	}
	s := &Server{
		port:            cfg.Port,
		host:            cfg.Host,
		shutdownTimeout: cfg.ShutdownTimeout,
		card:            cfg.AgentCard,
		store:           a2a.NewTaskStore(),
		handlers:        make(map[string]Handler),
		sseHandlers:     make(map[string]SSEHandler),
		authMiddleware:  cfg.AuthMiddleware,
		allowedOrigins:  allowedOrigins,
		rateLimit:       cfg.RateLimit,
	}
	if s.rateLimit == nil {
		s.rateLimit = defaultRateLimitConfig()
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

	// Build handler chain: CORS → Security Headers → Auth → Rate Limit → Mux
	// CORS is outermost so OPTIONS preflight is handled before auth.
	var handler http.Handler = mux
	handler = rateLimitMiddleware(s.rateLimit)(handler)
	if s.authMiddleware != nil {
		handler = s.authMiddleware(handler)
	}
	handler = securityHeadersMiddleware(handler)
	handler = newCORSMiddleware(s.allowedOrigins)(handler)

	s.srv = &http.Server{
		Handler:        handler,
		WriteTimeout:   0, // SSE-safe: no write deadline
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MiB max header size
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
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20) // 2 MiB

	var req a2a.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || isMaxBytesError(err) {
			writeJSON(w, http.StatusRequestEntityTooLarge,
				a2a.NewErrorResponse(nil, a2a.ErrCodeParseError, "request body too large"))
			return
		}
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

// DefaultAllowedOrigins returns the default CORS origins for local development.
func DefaultAllowedOrigins() []string {
	return []string{
		"http://localhost",
		"https://localhost",
		"http://127.0.0.1",
		"https://127.0.0.1",
		"http://[::1]",
		"https://[::1]",
	}
}

// isOriginAllowed checks if the given origin matches the allowlist.
// Supports exact match and prefix+port matching (e.g. "http://localhost" matches
// "http://localhost:3000"). If the allowed list contains "*", all origins pass.
func isOriginAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	for _, a := range allowed {
		if a == "*" {
			return true
		}
		if strings.EqualFold(origin, a) {
			return true
		}
		// Prefix+colon match for port variants: "http://localhost" matches "http://localhost:3000"
		if strings.HasPrefix(strings.ToLower(origin), strings.ToLower(a)+":") {
			return true
		}
	}
	return false
}

// newCORSMiddleware returns CORS middleware that restricts origins to the allowlist.
// When the allowlist contains "*", it behaves as a wildcard (Access-Control-Allow-Origin: *).
// Otherwise it echoes the matched origin and adds Vary: Origin.
func newCORSMiddleware(allowed []string) func(http.Handler) http.Handler {
	hasWildcard := false
	for _, a := range allowed {
		if a == "*" {
			hasWildcard = true
			break
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if hasWildcard {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			} else if isOriginAllowed(origin, allowed) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Vary", "Origin")
			}
			// Non-matching origins: no CORS headers added

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeadersMiddleware adds security headers to every response.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
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

// isMaxBytesError checks if the error chain contains an http.MaxBytesError
// or the canonical "http: request body too large" message. This handles cases
// where json.Decoder wraps the MaxBytesError.
func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "http: request body too large")
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

// defaultRateLimitConfig returns sensible defaults for the A2A server.
func defaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		ReadRPS:    1.0,         // ~60/min
		ReadBurst:  10,          //
		WriteRPS:   10.0 / 60.0, // ~10/min
		WriteBurst: 3,           //
	}
}

// visitor tracks per-IP rate limiters.
type visitor struct {
	readLimiter  *rate.Limiter
	writeLimiter *rate.Limiter
	lastSeen     time.Time
}

// rateLimitMiddleware returns middleware that enforces per-IP rate limits.
// GET/HEAD/OPTIONS use the read limiter; POST/PUT/DELETE use the write limiter.
// Returns 429 with Retry-After header when the limit is exceeded.
func rateLimitMiddleware(cfg *RateLimitConfig) func(http.Handler) http.Handler {
	var mu sync.Mutex
	visitors := make(map[string]*visitor)

	// Background goroutine to evict stale visitors every 3 minutes.
	go func() {
		ticker := time.NewTicker(3 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			for ip, v := range visitors {
				if time.Since(v.lastSeen) > 5*time.Minute {
					delete(visitors, ip)
				}
			}
			mu.Unlock()
		}
	}()

	getVisitor := func(ip string) *visitor {
		mu.Lock()
		defer mu.Unlock()
		v, ok := visitors[ip]
		if !ok {
			v = &visitor{
				readLimiter:  rate.NewLimiter(rate.Limit(cfg.ReadRPS), cfg.ReadBurst),
				writeLimiter: rate.NewLimiter(rate.Limit(cfg.WriteRPS), cfg.WriteBurst),
			}
			visitors[ip] = v
		}
		v.lastSeen = time.Now()
		return v
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract IP from RemoteAddr (host:port).
			ip := r.RemoteAddr
			if host, _, err := net.SplitHostPort(ip); err == nil {
				ip = host
			}

			v := getVisitor(ip)
			var limiter *rate.Limiter
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				limiter = v.readLimiter
			default:
				limiter = v.writeLimiter
			}

			if !limiter.Allow() {
				retryAfter := math.Ceil(1.0 / float64(limiter.Limit()))
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter)))
				writeJSON(w, http.StatusTooManyRequests,
					a2a.NewErrorResponse(nil, a2a.ErrCodeInternal, "rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func init() {
	// Suppress default log timestamp for cleaner output
	log.SetFlags(0)
}
