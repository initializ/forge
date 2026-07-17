package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
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
	// Platform is the managed token-resolver wiring, required by servers
	// with auth.type=platform. nil → no platform servers in this config.
	Platform *types.PlatformConfig
}

// knownMCPAuthTypes is the closed set of accepted Auth.Type values.
// MUST stay in sync with validate.knownMCPAuthTypes — the validate
// package is the canonical source for the YAML-config path, but
// NewServer re-checks here so Go-API callers constructing a
// types.MCPServer programmatically (and skipping ValidateMCPConfig)
// still get a loud failure for typos like "Bearer" (capital B)
// instead of a silently-unauthenticated transport. Review B6.
//
// Duplicated as a literal map rather than imported from validate to
// avoid coupling forge-core/mcp to forge-core/validate. If the set
// of types changes, both copies must move together — covered by
// TestB6_KnownAuthTypes_MatchValidate.
var knownMCPAuthTypes = map[string]bool{
	"bearer":   true,
	"static":   true,
	"oauth":    true,
	"platform": true, // managed: agent-principal token from the platform resolver
	"user":     true, // managed: delegated user identity (lazy; #317)
}

// NewServer constructs a Server. Returns an error when:
//
//   - spec.Auth.Type is set to an unknown value (review B6) — a typo
//     like "Bearer" capitalized would otherwise fall through
//     buildAuthFn and produce an unauthenticated transport.
//   - spec.Auth.Type is "bearer" or "static" with an empty TokenEnv
//     — runtime would silently send "" as the bearer token.
//   - spec requires oauth but no OAuthFlow was supplied.
//   - spec requires oauth but ClientID / AuthorizeURL / TokenURL are
//     empty.
//
// The validate package catches all of these on the YAML path;
// these checks make NewServer safe for programmatic construction
// too. Without them, the only signal of a misconfiguration was a
// distant 401/403 from the remote server.
func NewServer(spec types.MCPServer, deps ServerDeps) (*Server, error) {
	if spec.Auth != nil {
		if spec.Auth.Type == "" {
			return nil, fmt.Errorf("%w: server %q: auth.type is required when auth block is set", ErrProtocolError, spec.Name)
		}
		if !knownMCPAuthTypes[spec.Auth.Type] {
			return nil, fmt.Errorf("%w: server %q: unknown auth.type %q (must be one of: bearer, static, oauth, platform, user)", ErrProtocolError, spec.Name, spec.Auth.Type)
		}
		switch spec.Auth.Type {
		case "bearer", "static":
			if spec.Auth.TokenEnv == "" {
				return nil, fmt.Errorf("%w: server %q: auth.token_env is required for type=%s", ErrProtocolError, spec.Name, spec.Auth.Type)
			}
		case "oauth":
			if spec.Auth.Grant == grantClientCredentials {
				// #324: 2LO agent-principal needs an explicit client + secret
				// + token endpoint (no authorize endpoint, no DCR).
				if spec.Auth.ClientID == "" || spec.Auth.ClientSecretEnv == "" || spec.Auth.TokenURL == "" {
					return nil, fmt.Errorf("%w: server %q: grant client_credentials requires auth.client_id, auth.client_secret_env, auth.token_url", ErrProtocolError, spec.Name)
				}
			} else {
				// #316: the endpoints + client_id may be discovered
				// (RFC 9728/8414/7591) from the server URL, so the trio is no
				// longer required. Only reject a PARTIAL endpoint config —
				// authorize_url and token_url must be set together or both
				// omitted (both-omitted ⇒ discovery). A server with no URL
				// and no endpoints has nothing to discover from.
				if (spec.Auth.AuthorizeURL == "") != (spec.Auth.TokenURL == "") {
					return nil, fmt.Errorf("%w: server %q: auth.authorize_url and auth.token_url must be set together (or both omitted for discovery)", ErrProtocolError, spec.Name)
				}
				if spec.Auth.AuthorizeURL == "" && spec.URL == "" {
					return nil, fmt.Errorf("%w: server %q: oauth needs either explicit authorize_url/token_url or a url to discover them from", ErrProtocolError, spec.Name)
				}
			}
			if deps.OAuth == nil {
				return nil, fmt.Errorf("%w: server %q requires oauth but no OAuthFlow supplied", ErrProtocolError, spec.Name)
			}
		case "platform":
			// Agent-principal identity from the platform resolver. The
			// platform block is the contract — without it the server can
			// never authenticate, so fail construction, not the first call.
			if deps.Platform == nil || deps.Platform.TokenEndpoint == "" {
				return nil, fmt.Errorf("%w: server %q: auth.type=platform requires the top-level platform block (token_endpoint + agent_identity) — platform-materialized config", ErrProtocolError, spec.Name)
			}
		case "user":
			// Delegated user identity is INHERENTLY LAZY: there is no user
			// at startup. A Required user-server would deadlock startup on
			// a human — reject the combination (also caught by validate).
			if spec.Required {
				return nil, fmt.Errorf("%w: server %q: auth.type=user cannot be required:true — delegated identity connects lazily after consent", ErrProtocolError, spec.Name)
			}
			// #317: the delegated token resolves via the platform token
			// endpoint (per-user, {server, subject}) — so the platform block
			// is the contract, same as type=platform.
			if deps.Platform == nil || deps.Platform.TokenEndpoint == "" {
				return nil, fmt.Errorf("%w: server %q: auth.type=user requires the top-level platform block (delegated tokens resolve via the platform token endpoint)", ErrProtocolError, spec.Name)
			}
		}
	}
	logger := deps.Logger
	if logger == nil {
		logger = nopLogger{}
	}

	authFn := buildAuthFn(spec, deps)
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

			// Every error path moves through Degraded first — that's the
			// state machine's "we hit an error, deciding next step." From
			// Degraded we either Reconnect or terminal-Fail.
			//
			// runOnce can return error from one of {Connecting,
			// Initializing, Discovering, Ready}. All four are legal
			// predecessors of Degraded (review B1 fix).
			s.transition(StateDegraded)

			// Terminal: backoff exhausted.
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
			// Terminal: non-retryable errors (version mismatch, schema bug).
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

			// Retry: Degraded → Reconnecting → backoff → next runOnce
			// (which starts with transition(Connecting), legal from
			// Reconnecting).
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
		return withPhase("connect", err)
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
		return withPhase("initialize", err)
	}
	// Send the initialized notification per spec. Failure here means
	// the server did NOT receive the notification — strict MCP
	// servers will then reject the subsequent tools/list, masking
	// the real cause as a "discover" failure (review B5).
	// Propagate the error and bound it with the per-call timeout
	// (the notification is fire-and-forget but the underlying HTTP
	// POST still needs a cap).
	notifyCtx, notifyCancel := context.WithTimeout(ctx, s.timeout())
	if err := cli.Initialized(notifyCtx); err != nil {
		notifyCancel()
		closeAll()
		return withPhase("initialize", fmt.Errorf("initialized notification: %w", err))
	}
	notifyCancel()
	s.logger.Info("mcp server initialized", map[string]any{
		"server": s.Name, "server_version": res.ServerInfo.Version,
	})

	s.transition(StateDiscovering)
	listCtx, listCancel := context.WithTimeout(ctx, s.timeout())
	descs, err := cli.ListTools(listCtx)
	listCancel()
	if err != nil {
		closeAll()
		return withPhase("discover", fmt.Errorf("tools/list: %w", err))
	}

	// Validate every input schema AND every name before exposing the
	// server. A bad schema fails THIS server, not the LLM call. A bad
	// name (empty, or contains the "__" namespace separator — review
	// B9) also fails the server: the registry's contains-"__" check
	// would otherwise admit ambiguous names like "<server>__" or
	// "<server>____foo".
	for i, d := range descs {
		if d.Name == "" {
			closeAll()
			return withPhase("discover", fmt.Errorf("tool[%d]: descriptor name is empty", i))
		}
		if strings.Contains(d.Name, "__") {
			closeAll()
			return withPhase("discover", fmt.Errorf("tool[%d] %q: name contains \"__\" — reserved for the <server>__<tool> namespace separator", i, d.Name))
		}
		if err := ValidateInputSchema(d.InputSchema); err != nil {
			closeAll()
			return withPhase("discover", fmt.Errorf("tool[%d] %q: %w", i, d.Name, err))
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
		// Tag as "runtime" phase explicitly (post-Ready failure, not
		// a discover/initialize/connect-phase issue).
		return withPhase("runtime", fmt.Errorf("%w: demuxer exited", ErrTransportUnavailable))
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
// legal. An illegal transition is a programming bug:
//
//   - Under `go test` (testing.Testing() == true) it PANICS so the
//     bug surfaces immediately during CI.
//   - In a built binary it logs an error and force-transitions to
//     StateFailed so a misconfigured server cannot leave the Server
//     stuck in an invalid state silently — the previous behavior
//     (review B1) was to log + return WITHOUT updating state, which
//     left every failure-path Server stuck at e.g. StateInitializing
//     through the entire reconnect cycle.
func (s *Server) transition(to ServerState) {
	s.mu.Lock()
	from := s.state
	if isValidTransition(from, to) {
		s.state = to
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	msg := fmt.Sprintf("illegal mcp state transition for %q: %s → %s", s.Name, from, to)
	if testing.Testing() {
		panic(msg)
	}
	s.logger.Error(msg, map[string]any{
		"server": s.Name, "from": from.String(), "to": to.String(),
	})
	// Force into Failed so we don't keep emitting bogus audit events
	// from a stale state. Failed → Stopped is the only legal outbound,
	// which markTerminal + the Run loop's terminal path will handle.
	s.mu.Lock()
	s.state = StateFailed
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
// Precondition: spec.Auth (when non-nil) has a known Auth.Type —
// enforced by NewServer (review B6). If the precondition is
// violated (e.g., a unit test bypassing NewServer), this function
// returns an AuthTokenFunc that surfaces ErrProtocolError on first
// call rather than silently sending unauthenticated requests.
//
// os.Getenv lookups happen INSIDE the closure so changes to env at
// runtime (e.g., a K8s Secret rotated and the pod restarted) take
// effect on the next call without a Manager restart.
func buildAuthFn(spec types.MCPServer, deps ServerDeps) AuthTokenFunc {
	if spec.Auth == nil {
		return nil
	}
	flow := deps.OAuth
	switch spec.Auth.Type {
	case "bearer", "static":
		env := spec.Auth.TokenEnv
		return func(_ context.Context) (string, error) {
			return getenv(env), nil
		}
	case "oauth":
		cfg := OAuthServerConfig{
			ServerURL:    spec.URL, // for #316 discovery / persisted-registration lookup
			Grant:        spec.Auth.Grant,
			ClientID:     spec.Auth.ClientID,
			Scopes:       spec.Auth.Scopes,
			AuthorizeURL: spec.Auth.AuthorizeURL,
			TokenURL:     spec.Auth.TokenURL,
		}
		secretEnv := spec.Auth.ClientSecretEnv
		name := spec.Name
		return func(ctx context.Context) (string, error) {
			c := cfg
			if c.Grant == grantClientCredentials {
				// Resolve the client secret from env at call time so a
				// rotated Secret takes effect without a Manager restart
				// (mirrors the bearer/static token lookup). #324
				c.ClientSecret = getenv(secretEnv)
				if c.ClientSecret == "" {
					// The most common headless misconfig: the secret env var
					// is configured by NAME but the platform didn't inject a
					// value. Fail closed with a clear cause instead of letting
					// the AS reject client_secret="" as a misleading
					// "revoked" (#325 review finding 1).
					return "", fmt.Errorf("%w: server %q: client_secret_env %q resolved to an empty value — is the secret injected into the agent's environment?", ErrProtocolError, name, secretEnv)
				}
			}
			return flow.BearerToken(ctx, name, c)
		}
	case "platform":
		// Agent-principal (service) identity: short-lived access token
		// from the platform resolver, cached to TTL. The resource refresh
		// token never reaches this process (invariant 8).
		ref := spec.Auth.Ref
		if ref == "" {
			ref = spec.Name
		}
		src := newPlatformTokenSource(PlatformSourceConfig{
			TokenEndpoint: deps.Platform.TokenEndpoint,
			AgentIdentity: deps.Platform.AgentIdentity,
			Ref:           ref,
			HTTPClient:    deps.HTTPClient,
		})
		return src.Token
	case "user":
		// Delegated user identity (#317): resolve a per-REQUESTING-USER
		// token from the platform token endpoint. Lazy by design — until a
		// request carries an authenticated user, and until the platform has
		// a grant for that user, this fails with ErrNoToken so the server
		// never blocks startup.
		ref := spec.Auth.Ref
		if ref == "" {
			ref = spec.Name
		}
		src := newDelegatedTokenSource(PlatformSourceConfig{
			TokenEndpoint: deps.Platform.TokenEndpoint,
			AgentIdentity: deps.Platform.AgentIdentity,
			Ref:           ref,
			HTTPClient:    deps.HTTPClient,
		})
		serverName := spec.Name
		return func(ctx context.Context) (string, error) {
			subject := delegatedSubject(ctx)
			if subject == "" {
				return "", fmt.Errorf("%w: server %q uses delegated user identity but no requesting user is in context — it connects lazily under a user's session, never at startup", ErrNoToken, serverName)
			}
			return src.TokenForSubject(ctx, subject)
		}
	}
	// Defense in depth — NewServer should have rejected an unknown
	// auth.Type already. If a future refactor accidentally bypasses
	// that check, fail loud on the first request instead of sending
	// silently-unauthenticated traffic.
	unknownType := spec.Auth.Type
	serverName := spec.Name
	return func(_ context.Context) (string, error) {
		return "", fmt.Errorf("%w: server %q has unknown auth.type %q — buildAuthFn called without going through NewServer (review B6)",
			ErrProtocolError, serverName, unknownType)
	}
}

// delegatedSubject extracts the requesting user's stable identifier from
// the authenticated request context for the type=user resolver (#317).
// Email is preferred (portable, matches the §18 email-keyed model), then
// the user id. Empty when there is no authenticated user in ctx (e.g. a
// connection established at startup with no request behind it).
func delegatedSubject(ctx context.Context) string {
	id := auth.IdentityFromContext(ctx)
	if id == nil {
		return ""
	}
	if id.Email != "" {
		return id.Email
	}
	return id.UserID
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

// phasedError tags an error with the lifecycle phase that produced
// it. runOnce wraps each phase's failure via withPhase(), so
// classifyFailurePhase can dispatch via errors.As — no string
// matching (review B12, follow-up to B5).
type phasedError struct {
	phase string
	err   error
}

func (e *phasedError) Error() string { return e.phase + ": " + e.err.Error() }
func (e *phasedError) Unwrap() error { return e.err }
func (e *phasedError) Phase() string { return e.phase }

func withPhase(phase string, err error) error {
	return &phasedError{phase: phase, err: err}
}

// classifyFailurePhase returns the phase tag attached by withPhase
// at the failing site in runOnce. Any error not carrying a
// phasedError wrap (e.g., a panic-recovered error or a future
// code path we haven't tagged) classifies as "runtime".
//
// Prefix matching on err.Error() — the previous approach — broke
// when an upstream server's natural-language error text happened
// to overlap our prefix names (B5 reproduction). Using a typed
// wrap removes that fragility entirely.
func classifyFailurePhase(err error) string {
	if err == nil {
		return "unknown"
	}
	var pe *phasedError
	if errors.As(err, &pe) {
		return pe.Phase()
	}
	return "runtime"
}
