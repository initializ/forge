package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
)

// Handler processes a JSON-RPC request and returns a response.
type Handler func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse

// SSEHandler streams SSE events for a JSON-RPC request.
type SSEHandler func(ctx context.Context, id any, rawParams json.RawMessage, w http.ResponseWriter, flusher http.Flusher)

// RateLimitConfig controls per-IP rate limiting on the A2A server.
//
// Defaults (see defaultRateLimitConfig): 60/min for both reads and
// writes, with burst 10 / 20 respectively, and tasks/cancel exempt
// from the write bucket entirely. The bumped write defaults (vs the
// original #31 design of 10/min) reflect operational reality —
// orchestrated parallel-task dispatch and cron-fire bursts blow past
// the old 10/min in seconds. The cancel exemption is the most
// important change: cancel is "stop doing work," it's the recovery
// mechanism for runaway invocations, and throttling it amplifies the
// problem it's trying to solve. See issue #110 / FWS-10.
type RateLimitConfig struct {
	ReadRPS    float64 // requests per second for read operations (GET/HEAD/OPTIONS)
	ReadBurst  int     // burst size for reads
	WriteRPS   float64 // requests per second for write operations (POST/PUT/DELETE)
	WriteBurst int     // burst size for writes
	// CancelExempt skips the write limiter for `tasks/cancel` JSON-RPC
	// requests entirely. Default true. The cost-ceiling cancel-burst
	// case (orchestrator firing N parallel cancels when a workflow
	// budget trips) is the canonical example of why this exists —
	// sharing the write bucket with tasks/send means the cancels are
	// throttled at exactly the moment cancellation matters most.
	// DoS via cancel-spam is naturally bounded by the registry's
	// O(1) unknown-task lookup. See issue #110 / FWS-10.
	CancelExempt bool
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

	// Register core A2A handlers. The canonical Agent Card path is
	// /.well-known/agent-card.json per A2A 0.3.0; the legacy
	// /.well-known/agent.json path is also served (with a Deprecation
	// response header) so existing clients keep working through one
	// release cycle.
	mux.HandleFunc("GET /.well-known/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentCardLegacy)
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

// handleAgentCardLegacy serves the same Agent Card payload on the
// legacy /.well-known/agent.json path with a Deprecation response
// header per RFC 8594, pointing clients at the A2A 0.3.0 canonical
// path. Removable after one release cycle.
func (s *Server) handleAgentCardLegacy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</.well-known/agent-card.json>; rel="successor-version"`)
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

	// Phase 5 (#106) — extract the inbound W3C tracecontext + baggage
	// BEFORE wrapping with workflow context so the dispatcher span
	// becomes a CHILD of the upstream caller's span when a
	// `traceparent` header is present. Multi-hop A2A flows
	// (orchestrator → agent → downstream agent) then display as a
	// single trace in the backend instead of N disconnected roots.
	//
	// The propagator is the composite TraceContext + Baggage installed
	// on the OTel global by Phase 0's SetTracerProvider. When the
	// inbound request has NO traceparent header the propagator returns
	// the ctx unchanged — and Tracer().Start below opens a new root,
	// matching the pre-Phase-5 behavior verbatim.
	ctx := otel.GetTextMapPropagator().Extract(
		r.Context(),
		propagation.HeaderCarrier(r.Header),
	)

	// Extract initializ orchestration headers (issue #86 / FWS-2) ONCE
	// at the dispatch boundary so every downstream handler sees the
	// same WorkflowContext via ctx without having to parse headers
	// itself. Absent headers produce an IsZero WorkflowContext —
	// audit events then omit the workflow fields, matching pre-FWS-2
	// shape (backward compatible).
	ctx = coreruntime.WithWorkflowContext(ctx,
		coreruntime.WorkflowContextFromHTTPHeaders(r.Header))

	// Extract per-request tenancy override headers (#157) at the same
	// boundary so EmitFromContext can prefer them over the static
	// deployment-time stamp installed via AuditLogger.WithTenancy.
	// Absent headers produce an IsZero TenancyContext — the static
	// stamp wins, or fields omit when no stamp is installed either.
	ctx = coreruntime.WithTenancyContext(ctx,
		coreruntime.TenancyContextFromHTTPHeaders(r.Header))

	// Phase 3 (#104) — open the inbound dispatch span. Span name
	// mirrors the JSON-RPC method ("a2a.tasks/send", "a2a.tasks/get",
	// "a2a.tasks/cancel") so backend dashboards key by the same
	// vocabulary the audit events use. When tracing is disabled the
	// noop tracer returns a non-recording span and the SetAttributes /
	// End calls are near-zero cost.
	//
	// The span sits ABOVE the SSE/handler branches so it covers both —
	// streaming and unary methods share the same dispatch envelope.
	// Per-iteration LLM and tool spans live in the executor (Phase 3
	// continues in forge-core/runtime/loop.go); this is the root for
	// every inbound A2A request — OR a child of the upstream span
	// when Phase 5's propagator extracted one above.
	ctx, span := coreruntime.Tracer().Start(ctx, "a2a."+req.Method,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String(observability.AttrForgeA2AMethod, req.Method)),
	)
	defer span.End()
	if wf := coreruntime.WorkflowContextFromContext(ctx); !wf.IsZero() {
		// FWS-2 orchestrator correlation surfaces on the span so a
		// trace browser can pivot from a workflow run to every Forge
		// agent invocation that workflow triggered. Both definition
		// and execution ids land so observability backends can build
		// "all spans for this run" and "all spans for this workflow
		// over time" views without a join on opaque ids. FORGE-2 /
		// issue #185.
		attrs := []attribute.KeyValue{
			attribute.String(observability.AttrForgeWorkflowID, wf.WorkflowID),
			attribute.String(observability.AttrForgeWorkflowStageID, wf.StageID),
			attribute.String(observability.AttrForgeWorkflowStepID, wf.StepID),
		}
		if wf.WorkflowExecutionID != "" {
			attrs = append(attrs,
				attribute.String(observability.AttrForgeWorkflowExecutionID, wf.WorkflowExecutionID))
		}
		span.SetAttributes(attrs...)
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
		h(ctx, req.ID, req.Params, w, flusher)
		return
	}

	// Check regular handlers
	if h, ok := s.handlers[req.Method]; ok {
		// Attach a per-request response-header stage so the handler
		// can publish FWS-3 X-Forge-* invocation-usage headers (or
		// future per-method headers) without needing access to the
		// http.ResponseWriter, which the JSON-RPC Handler signature
		// deliberately omits. The dispatcher drains the stage onto
		// the writer's Header() before writeJSON emits the body.
		ctx = WithResponseHeaderStage(ctx)
		resp := h(ctx, req.ID, req.Params)
		DrainResponseHeaderStage(ctx, w.Header())
		if resp != nil && resp.Error != nil {
			// JSON-RPC errors surface as Error/Ok on the span (the
			// HTTP response itself is still 200 — JSON-RPC semantics).
			// Numeric code stays attribute-only; descriptive text goes
			// on the status so trace browsers display it inline.
			span.SetAttributes(attribute.Int("rpc.jsonrpc.error_code", resp.Error.Code))
			span.SetStatus(codes.Error, resp.Error.Message)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Method not found also surfaces as a span error so an operator
	// scanning traces sees the misroute without having to grep the body.
	span.SetStatus(codes.Error, "method not found: "+req.Method)
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
//
// Write defaults bumped from the original #31 design (10/min, burst 3)
// to 60/min, burst 20 — large enough to absorb orchestrated
// parallel-task dispatch and cron-fire bursts without silent throttling,
// small enough to still bound anonymous-public-internet DoS at
// 1 task/sec sustained. Cancel is exempt by default; see RateLimitConfig.
// See issue #110 / FWS-10.
func defaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		ReadRPS:      1.0, // 60/min
		ReadBurst:    10,
		WriteRPS:     1.0, // 60/min (was 10/60 ≈ 10/min)
		WriteBurst:   20,  // (was 3)
		CancelExempt: true,
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

			// FWS-10: tasks/cancel exemption. Only POST requests to "/"
			// carry a JSON-RPC envelope, so the peek is gated on that —
			// no body-read for /healthz, /.well-known/*, GET /. The
			// peek caps at 4 KiB so a malicious caller can't force the
			// middleware to buffer a giant body; if the cap is hit
			// without finding the method field, we fall back to the
			// standard write classification.
			if cfg.CancelExempt && r.Method == http.MethodPost && r.URL.Path == "/" {
				if isTasksCancel(r) {
					next.ServeHTTP(w, r)
					return
				}
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

// jsonRPCPeekCap bounds how many bytes the rate-limit middleware
// reads off the request body when checking whether it's a tasks/cancel
// call. 4 KiB is generous for the `method` field (typically the body
// preamble is well under 100 bytes) without giving a malicious caller
// a knob to force unbounded buffering.
const jsonRPCPeekCap = 4 << 10

// isTasksCancel returns true when the request body's JSON-RPC `method`
// field is "tasks/cancel". The body is read up to jsonRPCPeekCap bytes
// and then restored via io.NopCloser so downstream handlers see the
// full payload. Any error (read failure, unparseable JSON, method
// missing) returns false — the caller falls back to standard write
// classification.
func isTasksCancel(r *http.Request) bool {
	if r.Body == nil {
		return false
	}
	buf := make([]byte, jsonRPCPeekCap)
	n, _ := io.ReadFull(r.Body, buf)
	body := buf[:n]
	// Drain any tail past the cap so r.Body.Close() doesn't leak the
	// connection — and stitch the cap'd prefix back into a new reader.
	rest, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(append(body, rest...)))

	var env struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	return env.Method == "tasks/cancel"
}

func init() {
	// Suppress default log timestamp for cleaner output
	log.SetFlags(0)
}
