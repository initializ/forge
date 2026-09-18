// Package optimizer implements the engine behind `forge optimizer`: a local
// context optimizer that Claude Code (or any Anthropic Messages API client)
// routes through by setting ANTHROPIC_BASE_URL to this server.
//
// It always meters token usage from the response (real billed counts) and, when
// a Compressor is configured, compresses the live zone of outbound /v1/messages
// bodies before forwarding. The pieces it establishes:
//
//   - ANTHROPIC_BASE_URL routing works and Claude Code drives the server;
//   - streaming (SSE) fidelity is preserved end-to-end (no buffering stalls);
//   - real billed token usage can be extracted from the response;
//   - gateway chaining works — if the org already points ANTHROPIC_BASE_URL at
//     their own gateway, the optimizer sits in front and forwards to it;
//   - reversible context compression (ctxzip) runs on the outbound live zone
//     (see compress.go), working on raw JSON and preserving cache determinism;
//   - context_expand retrieval, either via an MCP endpoint (see expand_mcp.go)
//     or resolved server-side in-band with no client setup (see
//     inband_expand.go).
//
// Still to come: intelligent context optimization beyond ctxzip.
package optimizer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultUpstream is the Anthropic Messages API base URL used when no upstream
// is configured and ANTHROPIC_BASE_URL is unset.
const DefaultUpstream = "https://api.anthropic.com"

// ServiceName identifies this server in the /healthz response so a wrapper can
// confirm an address is a forge optimizer (and not some other service) before
// attaching to it.
const ServiceName = "forge-optimizer"

// DefaultListen is the loopback address the optimizer binds by default. It is
// loopback-only on purpose: the server forwards the caller's Authorization /
// x-api-key untouched and adds no auth of its own, so trust derives from the
// localhost boundary (same discipline as the audit HTTP sink).
const DefaultListen = "127.0.0.1:8787"

// Config configures the optimizer server.
type Config struct {
	// Listen is the bind address (host:port). Default DefaultListen.
	Listen string
	// Upstream is the base URL requests are forwarded to. Default
	// DefaultUpstream. Set to an org's existing gateway for chaining.
	Upstream string
	// Reporter receives one Report per proxied request. Default NopReporter.
	Reporter Reporter
	// Compressor, when non-nil, compresses the live zone of outbound
	// /v1/messages request bodies. Nil disables compression (pure metering).
	Compressor *Compressor
	// InbandExpand, when true (and a Compressor is set), injects the
	// context_expand tool into requests and resolves the model's calls to it
	// server-side — making retrieval zero-config (no `claude mcp add`). See
	// inband_expand.go.
	InbandExpand bool
	// Memory, when non-nil, ambiently forms episodic memory by distilling
	// completed task spans off the wire (see memory_formation.go). Nil disables
	// memory formation.
	Memory *MemoryFormer
	// Logger is optional; nil uses slog.Default().
	Logger *slog.Logger
}

// Server is the optimizer HTTP server.
type Server struct {
	listen       string
	upstream     *url.URL
	client       *http.Client
	inbandClient *http.Client
	reporter     Reporter
	compressor   *Compressor
	inbandExpand bool
	memory       *MemoryFormer
	stats        *StatsReporter
	log          *slog.Logger
	seq          atomic.Uint64
}

// New validates cfg and constructs a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Upstream == "" {
		cfg.Upstream = DefaultUpstream
	}
	u, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("optimizer: parsing upstream %q: %w", cfg.Upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("optimizer: upstream %q must be an absolute URL (scheme://host)", cfg.Upstream)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Warn loudly on a non-loopback bind. The proxy forwards the caller's
	// Authorization header untouched and serves /stats, so binding to a routable
	// interface turns it into an open forwarding proxy and a stats leak. The
	// default is loopback; an explicit override is allowed (trusted, access-
	// controlled network / container) but must never be silent.
	if host, _, err := net.SplitHostPort(cfg.Listen); err == nil && !isLoopbackHost(host) {
		logger.Warn("optimizer: binding to a NON-LOOPBACK address exposes an open forwarding proxy (forwards caller auth) and /stats to the network — bind 127.0.0.1 unless this is a trusted, access-controlled network",
			"listen", cfg.Listen)
	}

	// The stats reporter is always wired in so local running totals are
	// available at /stats regardless of what the caller configured. Any
	// caller-supplied reporter (log, file, control plane) runs alongside it.
	stats := NewStatsReporter(0)
	reporter := Reporter(stats)
	if cfg.Reporter != nil {
		reporter = MultiReporter{stats, cfg.Reporter}
	}

	// DisableCompression + stripping Accept-Encoding upstream keeps the wire
	// plaintext so usage extraction can read the SSE/JSON body, while the
	// forwarded bytes stay verbatim (no transparent gunzip that would then be
	// re-sent without its Content-Encoding header). Streaming responses have no
	// overall deadline; per-attempt timeouts live on dial/TLS/response-header.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true,
	}

	// The in-band expansion path forces the upstream request to NON-streaming so
	// it can inspect the whole turn for context_expand calls. For a long
	// generation the response headers only arrive after the entire message is
	// produced, which trips the 60s ResponseHeaderTimeout above and surfaces as a
	// 502 → client retry. So the in-band forward uses a client with NO header
	// timeout; the request context still bounds it (client disconnect cancels).
	inbandTransport := transport.Clone()
	inbandTransport.ResponseHeaderTimeout = 0

	return &Server{
		listen:       cfg.Listen,
		upstream:     u,
		client:       &http.Client{Transport: transport},
		inbandClient: &http.Client{Transport: inbandTransport},
		reporter:     reporter,
		compressor:   cfg.Compressor,
		inbandExpand: cfg.InbandExpand && cfg.Compressor != nil,
		memory:       cfg.Memory,
		stats:        stats,
		log:          logger,
	}, nil
}

// Upstream returns the resolved upstream base URL (for logging / diagnostics).
func (s *Server) Upstream() string { return s.upstream.String() }

// Handler returns the HTTP handler: a health endpoint plus the catch-all
// pass-through proxy.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// The "service" field lets `forge optimizer claude` confirm this is a
		// forge optimizer before attaching. "compress" tells it whether the
		// running instance is compressing (so the wrapper can warn if the
		// user's --compress* flags won't take effect on an attach).
		_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"compress":%t}`, ServiceName, s.compressor != nil)
	})
	// Local usage view — running totals + recent requests as JSON. Anthropic
	// clients only call /v1/*, so this local-only route never shadows a
	// proxied path.
	mux.Handle("/stats", s.stats)
	// context_expand MCP endpoint — only when compression is on (it shares the
	// compressor's store, the sole owner of the bbolt file).
	if s.compressor != nil {
		mux.Handle(expandMCPPath, newExpandMCPHandler(s.compressor.Store(), s.stats, s.log))
	}
	mux.HandleFunc("/", s.proxy)
	return mux
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully.
// isLoopbackHost reports whether a bind host is loopback-only. An empty host
// (":8787") binds all interfaces, so it is NOT loopback. "localhost" is treated
// as loopback; any other non-IP hostname could resolve anywhere, so it's treated
// as non-loopback (warn rather than assume safe).
func isLoopbackHost(host string) bool {
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.listen,
		Handler: s.Handler(),
		// No global timeouts: streaming responses can outlast any fixed
		// deadline. The transport bounds the risky phases (dial/TLS/headers).
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("optimizer listening",
			"listen", s.listen,
			"upstream", s.upstream.String(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// hopByHopHeaders are per-connection headers that must not be forwarded by a
// proxy (RFC 7230 §6.1). Content-Encoding is dropped too because we strip
// Accept-Encoding upstream and forward decoded bytes.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// proxy forwards one request to the upstream and meters the response. On any
// forwarding error it returns 502 and reports the failure — it never masks the
// client's own auth or alters the body.
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cid := correlationID(r, s.seq.Add(1))
	client := detectClient(r.Header.Get("User-Agent"))

	target := *s.upstream
	target.Path = singleJoiningSlash(s.upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	// For POST /v1/messages we buffer the body to (a) key stats by session,
	// (b) compress the live zone, and (c) inject the context_expand tool.
	// Everything else streams through untouched.
	var (
		reqBody          io.Reader = r.Body
		bodyLen                    = r.ContentLength
		compStats        CompressStats
		sessionID        string
		recalledEpisodes int
		outBody          []byte // non-nil once we've buffered/modified the body
	)
	if r.Method == http.MethodPost && isMessagesPath(r.URL.Path) {
		raw, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			s.fail(w, r, cid, "", start, http.StatusBadGateway, "read request body", rerr)
			return
		}
		sessionID = deriveSessionID(r.Header, raw)
		outBody = raw
		// Ambient memory formation runs on the ORIGINAL uncompressed body (the
		// full readable transcript) and is fully async — never gates the proxy.
		if s.memory != nil {
			s.memory.Observe(sessionID, raw, r.Header, s.upstream.String())
		}
		if s.compressor != nil {
			out, st, _ := s.compressor.Transform(outBody)
			outBody = out
			compStats = st
		}
		if s.inbandExpand {
			outBody = injectExpandTool(outBody)
		}
		// Recall injection runs last so it operates on the final body. The
		// injected block is frozen per session, so it does not disturb cache
		// breakpoints across turns (see memory_recall.go).
		if s.memory != nil {
			if injected, n := s.memory.Inject(sessionID, outBody); n > 0 {
				outBody = injected
				recalledEpisodes = n
			}
		}
		reqBody = bytes.NewReader(outBody)
		bodyLen = int64(len(outBody))
	}

	// In-band expansion: when the outbound body carries a compression marker,
	// the model may call context_expand. Buffer + resolve server-side so Claude
	// Code never sees the tool (zero-config retrieval).
	if s.inbandExpand && outBody != nil && bodyHasCtxzipMarkers(outBody) {
		s.interceptAndForward(w, r, cid, sessionID, client, start, outBody, compStats)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), reqBody)
	if err != nil {
		s.fail(w, r, cid, sessionID, start, http.StatusBadGateway, "build upstream request", err)
		return
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Del("Accept-Encoding") // force plaintext upstream (see New)
	req.ContentLength = bodyLen
	req.Host = s.upstream.Host

	resp, err := s.client.Do(req)
	if err != nil {
		s.fail(w, r, cid, sessionID, start, http.StatusBadGateway, "forward to upstream", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	var usage Usage
	streaming := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if streaming {
		usage = s.streamThrough(w, resp.Body)
		usage.Streaming = true
	} else {
		usage = s.bufferThrough(w, resp.Body)
	}

	s.reporter.Report(r.Context(), Report{
		CorrelationID:    cid,
		SessionID:        sessionID,
		Client:           client,
		Method:           r.Method,
		Path:             r.URL.Path,
		StatusCode:       resp.StatusCode,
		Streaming:        streaming,
		Usage:            usage,
		Compression:      compStats,
		RecalledEpisodes: recalledEpisodes,
		Upstream:         s.upstream.String(),
		DurationMS:       time.Since(start).Milliseconds(),
	})
}

// isMessagesPath reports whether path is the Anthropic Messages endpoint (and
// not the /v1/messages/count_tokens sub-path, which the compressor leaves
// alone).
func isMessagesPath(path string) bool {
	return strings.HasSuffix(path, "/v1/messages")
}

// streamThrough copies an SSE body to the client with per-chunk flushing while
// parsing token usage out of the event stream. Bytes reach the client verbatim
// and immediately — metering rides alongside, never gating the stream.
func (s *Server) streamThrough(w http.ResponseWriter, body io.Reader) Usage {
	flusher, _ := w.(http.Flusher)
	p := newSSEUsageParser()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				// Client hung up; stop copying but keep whatever usage we saw.
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			p.feed(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	return p.usage()
}

// bufferThrough copies a non-streaming body to the client and parses usage from
// the complete JSON response.
func (s *Server) bufferThrough(w http.ResponseWriter, body io.Reader) Usage {
	data, err := io.ReadAll(body)
	if err != nil {
		_, _ = w.Write(data)
		return Usage{}
	}
	_, _ = w.Write(data)
	return parseJSONUsage(data)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, cid, sessionID string, start time.Time, code int, stage string, err error) {
	s.log.Error("optimizer forward failed", "cid", cid, "stage", stage, "error", err.Error())
	http.Error(w, fmt.Sprintf("optimizer: %s: %v", stage, err), code)
	s.reporter.Report(r.Context(), Report{
		CorrelationID: cid,
		SessionID:     sessionID,
		Method:        r.Method,
		Path:          r.URL.Path,
		StatusCode:    code,
		Upstream:      s.upstream.String(),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         err.Error(),
	})
}

// correlationID prefers an inbound request id (so logs correlate with the
// caller) and otherwise mints one from random bytes plus a monotonic sequence.
func correlationID(r *http.Request, seq uint64) string {
	for _, h := range []string{"X-Request-Id", "Request-Id", "Anthropic-Request-Id"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("opt-%s-%d", hex.EncodeToString(b[:]), seq)
	}
	return fmt.Sprintf("opt-%d", seq)
}

// singleJoiningSlash joins two URL paths with exactly one slash, so an upstream
// base that already carries a path (e.g. an org gateway at /anthropic) composes
// correctly with the incoming /v1/messages.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}
