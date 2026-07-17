package runtime

import (
	"net/http"
	"sync"
	"time"

	"github.com/initializ/forge/forge-cli/server"
	"github.com/initializ/forge/forge-core/llm/oauth"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/authgate"
)

// This file is the STANDALONE-mode consent loop (design-tool-registry.md
// §18.4). In managed mode the platform hosts the callback + token custody
// and only signals Forge via POST /mcp/consent (mcp_authgate.go). In
// standalone mode Forge hosts its own loopback callback here: it validates
// the OAuth `state` it issued, exchanges the code for a token via the
// injected completer, and resumes the parked call.
//
// The security-critical piece is the state binding: a callback is honored
// only if its `state` was issued by us, hasn't been used, hasn't expired,
// and arrives in the same session that started the flow — rejecting
// cross-session, replayed, and stale callbacks (the #317/#330 acceptance).

// stateBinding is what an issued OAuth `state` is bound to. The callback
// must match all of it (modulo an empty session) to be honored.
type stateBinding struct {
	subject  string
	server   string
	session  string // the initiating session; callback must match (cross-session guard)
	deadline time.Time
}

// stateBinder issues single-use, expiring OAuth `state` values bound to the
// {subject, server, session} that initiated a consent flow, and validates
// them on the callback. In-process (like the gate itself); a restart drops
// pending flows, which is safe — the user just re-initiates.
type stateBinder struct {
	mu  sync.Mutex
	m   map[string]stateBinding
	ttl time.Duration
	now func() time.Time
}

// defaultStateTTL bounds how long an issued state is honored — long enough
// to click through an IdP consent screen, short enough that a leaked state
// is quickly useless.
const defaultStateTTL = 10 * time.Minute

func newStateBinder(ttl time.Duration) *stateBinder {
	if ttl <= 0 {
		ttl = defaultStateTTL
	}
	return &stateBinder{m: make(map[string]stateBinding), ttl: ttl, now: time.Now}
}

// Issue mints a cryptographically-random state bound to {subject, server,
// session} and returns it for embedding in the authorize URL.
func (s *stateBinder) Issue(subject, server, session string) (string, error) {
	state, err := oauth.GenerateState()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked() // opportunistic GC so abandoned flows don't accumulate
	s.m[state] = stateBinding{subject: subject, server: server, session: session, deadline: s.now().Add(s.ttl)}
	return state, nil
}

// Consume looks up and REMOVES the binding for state. It returns ok=false
// for an unknown state (forged), an already-used state (replay — the entry
// is gone after the first Consume), or an expired one. Single-use + expiry
// are enforced here; the caller enforces the session match.
func (s *stateBinder) Consume(state string) (stateBinding, bool) {
	if state == "" {
		return stateBinding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[state]
	if !ok {
		return stateBinding{}, false // unknown or already consumed (replay)
	}
	delete(s.m, state) // single-use: a replay of this state now finds nothing
	if s.now().After(b.deadline) {
		return stateBinding{}, false // expired
	}
	return b, true
}

// sweepLocked drops expired bindings. Caller holds s.mu.
func (s *stateBinder) sweepLocked() {
	now := s.now()
	for k, b := range s.m {
		if now.After(b.deadline) {
			delete(s.m, k)
		}
	}
}

// CallbackCompleter exchanges an OAuth authorization code for a token and
// stores it for {subject, server}, so the resumed call finds a grant. The
// standalone interactive resolver provides one; when nil the loopback
// callback is NOT registered (managed mode hosts its own callback and never
// hands Forge a code).
type CallbackCompleter func(subject, server, code string) error

// sessionFromRequest extracts the caller's session id for the cross-session
// check. Prefers an explicit forge session header; falls back to a session
// cookie. Empty ⇒ the handler skips the session match (still enforces
// single-use + expiry), which is the correct degradation when no session
// context is available (e.g. a purely CLI-driven loopback).
func sessionFromRequest(r *http.Request) string {
	if h := r.Header.Get("X-Forge-Session"); h != "" {
		return h
	}
	if c, err := r.Cookie("forge_session"); err == nil {
		return c.Value
	}
	return ""
}

// registerMCPCallbackEndpoint wires the standalone loopback callback. It is
// registered ONLY when a CallbackCompleter is set (standalone interactive
// mode); managed deployments never register it.
func (r *Runner) registerMCPCallbackEndpoint(srv *server.Server, auditLogger *coreruntime.AuditLogger) {
	if r.authGateEngine == nil || r.callbackCompleter == nil {
		return
	}
	if r.stateBinder == nil {
		r.stateBinder = newStateBinder(defaultStateTTL)
	}
	srv.RegisterHTTPHandler("GET /mcp/oauth/callback",
		makeMCPCallbackHandler(r.stateBinder, r.authGateEngine, r.callbackCompleter, sessionFromRequest, auditLogger))
}

// makeMCPCallbackHandler validates the state, enforces the session match,
// exchanges the code for a token, and resumes the parked call. Extracted so
// tests can drive every rejection path without a real IdP.
func makeMCPCallbackHandler(
	binder *stateBinder,
	engine *authgate.Engine,
	complete CallbackCompleter,
	sessionOf func(*http.Request) string,
	auditLogger *coreruntime.AuditLogger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		state, code := q.Get("state"), q.Get("code")
		if state == "" || code == "" {
			http.Error(w, "missing state or code", http.StatusBadRequest)
			return
		}
		// Single-use + expiry are enforced in Consume; unknown/replayed/
		// expired all collapse to !ok — a forged or stale callback can't
		// resume anyone.
		b, ok := binder.Consume(state)
		if !ok {
			http.Error(w, "invalid, expired, or already-used state", http.StatusBadRequest)
			return
		}
		// Cross-session guard: the callback must land in the session that
		// started the flow. A leaked/replayed state used from another
		// session is rejected.
		if b.session != "" {
			if got := sessionOf(req); got != b.session {
				http.Error(w, "state/session mismatch", http.StatusBadRequest)
				return
			}
		}
		// Exchange the code for a token and store it for {subject, server}.
		// Only AFTER this succeeds do we resume — resolving the gate with no
		// grant would just re-park the call (delegation follows
		// authorization: never resume before the grant exists).
		if err := complete(b.subject, b.server, code); err != nil {
			http.Error(w, "authorization exchange failed", http.StatusBadGateway)
			return
		}
		// Grant exists now → wake every call parked on {subject, server}.
		// A missing gate (call already timed out / was canceled) is benign:
		// the token is stored, so a fresh call will just succeed.
		_ = engine.Resolve(b.subject, b.server, authgate.DecisionGranted)
		if auditLogger != nil {
			auditLogger.Emit(coreruntime.AuditEvent{
				Event: coreruntime.EventMCPAuthResolved,
				Fields: map[string]any{
					"server": b.server, "subject": b.subject, "via": "loopback_callback",
				},
			})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><meta charset=utf-8><title>Connected</title>` +
			`<p>Authorization complete. You can return to your conversation.</p>`))
	}
}
