package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// defaultBackoff is the pinned reconnect schedule. Total cumulative
// wall-time after 5 failures: 31s, then StateFailed.
var defaultBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

// defaultTimeout is the per-call timeout when MCPServer.Timeout==0.
const defaultTimeout = 60 * time.Second

// clientFactory creates a fresh Client + Transport pair for a Server.
// Pulled out as a func type so server_test.go can inject mocks.
type clientFactory func(ctx context.Context) (Client, error)

// ServerLogger is a minimal logger interface — keeps the package free
// of a hard dependency on a particular logger implementation. The
// runtime.Logger interface satisfies it structurally.
type ServerLogger interface {
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

type nopLogger struct{}

func (nopLogger) Info(string, map[string]any)  {}
func (nopLogger) Warn(string, map[string]any)  {}
func (nopLogger) Error(string, map[string]any) {}

// Server wraps a single MCP server in its lifecycle state machine.
// One Server per entry in forge.yaml mcp.servers[]. Owned by the
// Manager (Commit 4); not constructed directly by application code.
//
// Lifecycle is driven by Run(ctx): a single goroutine progresses
// through Connecting → Initializing → Discovering → Ready, then sits
// in Ready until ctx is cancelled or a transport failure pushes it
// into Degraded → Reconnecting → (Failed or Initializing again).
//
// Tools is safe to call only AFTER state has reached Ready at least
// once.
type Server struct {
	Name string
	Spec types.MCPServer

	factory clientFactory
	logger  ServerLogger
	audit   *runtime.AuditLogger
	backoff []time.Duration

	mu     sync.Mutex
	state  ServerState
	tools  []MCPToolDescriptor
	client Client
	cancel context.CancelFunc

	ready        chan struct{} // closed when first Ready
	failed       chan struct{} // closed on terminal Failed/Stopped
	once         sync.Once     // for ready close
	terminalOnce sync.Once
}

// ServerDeps bundles the dependencies a Server needs. Pulled out so
// the Manager can hand the same set to every Server without verbose
// constructor signatures.
type ServerDeps struct {
	HTTPClient *http.Client // injected; never default
	Logger     ServerLogger // nil → no-op
	Audit      *runtime.AuditLogger
	OAuth      *OAuthFlow // nil → no OAuth servers in this config
}

// NewServer constructs a Server. Returns an error when the spec
// requires OAuth but no OAuthFlow was supplied.
func NewServer(spec types.MCPServer, deps ServerDeps) (*Server, error) {
	if spec.Auth != nil && spec.Auth.Type == "oauth" && deps.OAuth == nil {
		return nil, fmt.Errorf("%w: server %q requires oauth but no OAuthFlow supplied", ErrProtocolError, spec.Name)
	}
	logger := deps.Logger
	if logger == nil {
		logger = nopLogger{}
	}

	authFn := buildAuthFn(spec, deps.OAuth)
	factory := func(ctx context.Context) (Client, error) {
		tr, err := NewHTTPTransport(spec.URL, deps.HTTPClient, authFn)
		if err != nil {
			return nil, err
		}
		return NewClient(tr), nil
	}
	return &Server{
		Name:    spec.Name,
		Spec:    spec,
		factory: factory,
		logger:  logger,
		audit:   deps.Audit,
		backoff: defaultBackoff,
		state:   StateConfigured,
		ready:   make(chan struct{}),
		failed:  make(chan struct{}),
	}, nil
}

// State returns the current lifecycle state. Cheap; safe for
// concurrent use.
func (s *Server) State() ServerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Tools returns the filtered tool descriptors discovered during
// Discovering. Returns nil until the Server has reached Ready at
// least once.
func (s *Server) Tools() []MCPToolDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MCPToolDescriptor, len(s.tools))
	copy(out, s.tools)
	return out
}

// Client returns the underlying Client once Ready. nil before then.
// The Manager uses this to construct MCPTool adapters (Commit 4).
func (s *Server) Client() Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// Ready returns a channel closed when the Server first reaches the
// Ready state. Manager.Start waits on this.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Failed returns a channel closed when the Server reaches a terminal
// state (Failed or Stopped).
func (s *Server) Failed() <-chan struct{} { return s.failed }

// Run drives the state machine until ctx is cancelled. Returns a
// non-nil error only when the Server's Spec.Required is true AND the
// terminal state is Failed — the Manager interprets this as "kill the
// agent." Required=false failures return nil; the agent continues
// without this server's tools.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			s.transition(StateStopped)
			return nil
		default:
		}

		if err := s.runOnce(ctx); err != nil {
			s.logger.Warn("mcp server cycle ended with error", map[string]any{
				"server": s.Name, "error": err.Error(),
			})

			// Decide whether to retry or terminally fail.
			if attempt >= len(s.backoff) {
				s.emitFailed("backoff_exhausted", err)
				s.transition(StateFailed)
				s.markTerminal()
				if s.Spec.Required {
					return fmt.Errorf("required mcp server %q failed: %w", s.Name, err)
				}
				s.transition(StateStopped)
				return nil
			}
			// Some errors are not retryable — version mismatch, schema bug.
			if errors.Is(err, ErrVersionMismatch) {
				s.emitFailed("version_mismatch", err)
				s.transition(StateFailed)
				s.markTerminal()
				if s.Spec.Required {
					return fmt.Errorf("required mcp server %q failed: %w", s.Name, err)
				}
				s.transition(StateStopped)
				return nil
			}

			// Retry path.
			s.transition(StateReconnecting)
			s.emitDegraded(attempt+1, s.backoff[attempt])
			select {
			case <-ctx.Done():
				s.transition(StateStopped)
				return nil
			case <-time.After(s.backoff[attempt]):
			}
			attempt++
			continue
		}

		// Reached Ready successfully — reset backoff for next cycle.
		attempt = 0
		// Wait for ctx cancel or a transport error in the Run loop of
		// the demultiplexer. runOnce returns when either happens.
	}
}

// runOnce executes a single Connect→Initialize→Discover cycle and
// then blocks until the underlying client's demultiplexer exits or
// ctx is cancelled. Returns a non-nil error if any step failed; nil
// if the cycle ended cleanly (ctx cancel after Ready).
func (s *Server) runOnce(ctx context.Context) error {
	s.transition(StateConnecting)

	cli, err := s.factory(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	// Launch demultiplexer.
	demuxDone := make(chan struct{})
	go func() {
		defer close(demuxDone)
		// clientImpl exposes Run; bridge via type assertion.
		if r, ok := cli.(interface{ Run(context.Context) }); ok {
			r.Run(ctx)
		}
	}()

	closeAll := func() {
		_ = cli.Close()
		<-demuxDone
	}

	s.transition(StateInitializing)
	initCtx, initCancel := context.WithTimeout(ctx, s.timeout())
	res, err := cli.Initialize(initCtx, ClientInfo{Name: "forge-mcp", Version: "0.12.0"})
	initCancel()
	if err != nil {
		closeAll()
		return fmt.Errorf("initialize: %w", err)
	}
	// Send the initialized notification per spec.
	_ = cli.Initialized(ctx)
	s.logger.Info("mcp server initialized", map[string]any{
		"server": s.Name, "server_version": res.ServerInfo.Version,
	})

	s.transition(StateDiscovering)
	listCtx, listCancel := context.WithTimeout(ctx, s.timeout())
	descs, err := cli.ListTools(listCtx)
	listCancel()
	if err != nil {
		closeAll()
		return fmt.Errorf("tools/list: %w", err)
	}

	// Validate every input schema before exposing the server. A bad
	// schema fails THIS server, not the LLM call.
	for i, d := range descs {
		if err := ValidateInputSchema(d.InputSchema); err != nil {
			closeAll()
			return fmt.Errorf("tool[%d] %q: %w", i, d.Name, err)
		}
	}

	filtered := filterTools(descs, s.Spec.Tools)

	s.mu.Lock()
	s.tools = filtered
	s.client = cli
	s.mu.Unlock()

	s.transition(StateReady)
	s.emitStarted(len(filtered))
	s.once.Do(func() { close(s.ready) })

	// Block until demux done (transport failure) or ctx cancel.
	select {
	case <-ctx.Done():
		closeAll()
		return nil
	case <-demuxDone:
		// transport ended — error propagated via the demux's failAll.
		return fmt.Errorf("%w: demuxer exited", ErrTransportUnavailable)
	}
}

// timeout returns Spec.Timeout or defaultTimeout when zero.
func (s *Server) timeout() time.Duration {
	if s.Spec.Timeout == 0 {
		return defaultTimeout
	}
	return s.Spec.Timeout
}

// transition moves the state machine to `to`, asserting the move is
// legal. An illegal transition is a programming bug — panic to fail
// loud in tests; in prod the panic is recovered to a Failed state.
func (s *Server) transition(to ServerState) {
	s.mu.Lock()
	from := s.state
	if !isValidTransition(from, to) {
		s.mu.Unlock()
		s.logger.Error("illegal state transition", map[string]any{
			"server": s.Name, "from": from.String(), "to": to.String(),
		})
		return
	}
	s.state = to
	s.mu.Unlock()
}

// markTerminal closes the failed channel exactly once.
func (s *Server) markTerminal() {
	s.terminalOnce.Do(func() { close(s.failed) })
}

// FilterTools is the exported form of filterTools, for callers
// outside the package (e.g., `forge mcp list` which previews what a
// real `forge run` would expose).
func FilterTools(descs []MCPToolDescriptor, f types.MCPToolFilter) []MCPToolDescriptor {
	return filterTools(descs, f)
}

// filterTools applies Spec.Tools.{Allow,Deny} to the discovered set.
//
// Wildcard "*" in Allow expands to "every tool discovered at this
// connect" (snapshot semantics, decision §3.7 of the recommendations
// doc). Deny is subtractive against either explicit allow or the
// wildcard expansion.
func filterTools(descs []MCPToolDescriptor, f types.MCPToolFilter) []MCPToolDescriptor {
	allowAll := false
	allowSet := make(map[string]struct{}, len(f.Allow))
	for _, name := range f.Allow {
		if name == "*" {
			allowAll = true
			continue
		}
		allowSet[name] = struct{}{}
	}
	denySet := make(map[string]struct{}, len(f.Deny))
	for _, name := range f.Deny {
		denySet[name] = struct{}{}
	}

	out := make([]MCPToolDescriptor, 0, len(descs))
	for _, d := range descs {
		if _, denied := denySet[d.Name]; denied {
			continue
		}
		if allowAll {
			out = append(out, d)
			continue
		}
		if _, ok := allowSet[d.Name]; ok {
			out = append(out, d)
		}
	}
	return out
}

// buildAuthFn constructs the AuthTokenFunc for a server based on its
// Auth spec. Returns nil for no-auth servers (typical for in-cluster
// sidecars on trusted networks).
//
// os.Getenv lookups happen INSIDE the closure so changes to env at
// runtime (e.g., a K8s Secret rotated and the pod restarted) take
// effect on the next call without a Manager restart.
func buildAuthFn(spec types.MCPServer, flow *OAuthFlow) AuthTokenFunc {
	if spec.Auth == nil {
		return nil
	}
	switch spec.Auth.Type {
	case "bearer", "static":
		env := spec.Auth.TokenEnv
		return func(_ context.Context) (string, error) {
			return getenv(env), nil
		}
	case "oauth":
		cfg := OAuthServerConfig{
			ClientID:     spec.Auth.ClientID,
			Scopes:       spec.Auth.Scopes,
			AuthorizeURL: spec.Auth.AuthorizeURL,
			TokenURL:     spec.Auth.TokenURL,
		}
		name := spec.Name
		return func(ctx context.Context) (string, error) {
			return flow.BearerToken(ctx, name, cfg)
		}
	}
	return nil
}

// getenv is overridable for tests.
var getenv = func(k string) string {
	if k == "" {
		return ""
	}
	return os.Getenv(k)
}

// emitStarted / emitFailed / emitDegraded — audit helpers.

func (s *Server) emitStarted(toolCount int) {
	if s.audit == nil {
		return
	}
	s.audit.Emit(runtime.AuditEvent{
		Event:  runtime.EventMCPServerStarted,
		Fields: map[string]any{"name": s.Name, "transport": s.Spec.Transport, "tool_count": toolCount},
	})
}

func (s *Server) emitFailed(reason string, err error) {
	if s.audit == nil {
		return
	}
	phase := classifyFailurePhase(err)
	s.audit.Emit(runtime.AuditEvent{
		Event: runtime.EventMCPServerFailed,
		Fields: map[string]any{
			"name":   s.Name,
			"phase":  phase,
			"reason": reason,
		},
	})
}

func (s *Server) emitDegraded(attempt int, backoff time.Duration) {
	if s.audit == nil {
		return
	}
	s.audit.Emit(runtime.AuditEvent{
		Event: runtime.EventMCPServerDegraded,
		Fields: map[string]any{
			"name":       s.Name,
			"attempt":    attempt,
			"backoff_ms": backoff.Milliseconds(),
		},
	})
}

// classifyFailurePhase maps an error string to a reason code phase.
// Cheap, string-prefix-only — the phase is for ops dashboards, not
// programmatic dispatch.
func classifyFailurePhase(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connect"):
		return "connect"
	case strings.Contains(msg, "initialize"):
		return "initialize"
	case strings.Contains(msg, "tools/list"):
		return "discover"
	default:
		return "runtime"
	}
}
