package runtime

import (
	"context"
	"net/http"
	"strings"
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
	subject string
	server  string
	session string // the initiating session; callback must match (cross-session guard)
	// verifier is the PKCE code_verifier minted when the authorize URL was
	// built; the callback needs it to exchange the code (#332). Empty for
	// managed flows (Forge never sees the code) or where PKCE isn't used.
	verifier string
	// authorizeURL is the IdP authorize URL this state was minted for. The
	// GET /mcp/oauth/start endpoint redirects the browser here after setting
	// the session cookie (#332). Empty when Forge doesn't front the redirect.
	authorizeURL string
	deadline     time.Time
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
// session} and returns it for embedding in the authorize URL. Used where the
// caller doesn't front the redirect (no verifier/authorizeURL captured).
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

// Bind stores a fully-formed binding under a caller-provided state. The
// standalone front-half (#332) generates the state itself (it must embed it in
// the authorize URL before binding), then records the session, PKCE verifier,
// and authorize URL here so GET /mcp/oauth/start can set the session cookie and
// redirect, and GET /mcp/oauth/callback can complete the exchange.
func (s *stateBinder) Bind(state, subject, server, session, verifier, authorizeURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.m[state] = stateBinding{
		subject: subject, server: server, session: session,
		verifier: verifier, authorizeURL: authorizeURL,
		deadline: s.now().Add(s.ttl),
	}
}

// Peek returns the binding for state WITHOUT consuming it (unlike Consume),
// for the GET /mcp/oauth/start redirect: the state is spent later, at the
// callback. Returns ok=false for unknown or expired states.
func (s *stateBinder) Peek(state string) (stateBinding, bool) {
	if state == "" {
		return stateBinding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[state]
	if !ok || s.now().After(b.deadline) {
		return stateBinding{}, false
	}
	return b, true
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

// CallbackCompleter exchanges an OAuth authorization code (with the PKCE
// verifier bound to the state) for a token and stores it for {subject, server},
// so the resumed call finds a grant. The standalone resolver provides one; when
// nil the loopback callback is NOT registered (managed mode hosts its own
// callback and never hands Forge a code). ctx is the request context so the
// token exchange inherits a finite deadline.
type CallbackCompleter func(ctx context.Context, subject, server, code, verifier string) error

// sessionFromRequest extracts the caller's session id for the cross-session
// check. Prefers an explicit forge session header; falls back to a session
// cookie (the header isn't set on a browser redirect, so the cookie is the
// real path for the IdP callback). An empty result fails the mandatory
// session match in the handler — the network-exposed callback never
// downgrades to single-use+expiry alone.
func sessionFromRequest(r *http.Request) string {
	if h := r.Header.Get("X-Forge-Session"); h != "" {
		return h
	}
	if c, err := r.Cookie("forge_session"); err == nil {
		return c.Value
	}
	return ""
}

// registerMCPCallbackEndpoint wires the standalone consent endpoints. They are
// registered ONLY when a CallbackCompleter is set (standalone interactive
// mode); managed deployments never register them.
//
//   - GET /mcp/oauth/start: the link Forge delivers. It sets the forge_session
//     cookie (the producer the callback's mandatory session match requires) and
//     redirects the browser to the IdP authorize URL.
//   - GET /mcp/oauth/callback: the IdP redirect target. Validates state +
//     session, exchanges the code, resumes the parked call.
func (r *Runner) registerMCPCallbackEndpoint(srv *server.Server, auditLogger *coreruntime.AuditLogger) {
	if r.authGateEngine == nil || r.callbackCompleter == nil {
		return
	}
	if r.stateBinder == nil {
		r.stateBinder = newStateBinder(defaultStateTTL)
	}
	srv.RegisterHTTPHandler("GET /mcp/oauth/start", makeMCPStartHandler(r.stateBinder))
	srv.RegisterHTTPHandler("GET /mcp/oauth/callback",
		makeMCPCallbackHandler(r.stateBinder, r.authGateEngine, r.callbackCompleter, sessionFromRequest, auditLogger))
}

// forgeSessionCookie is the cookie name the callback's cross-session guard
// reads (sessionFromRequest). The start endpoint is its only producer.
const forgeSessionCookie = "forge_session"

// makeMCPStartHandler serves GET /mcp/oauth/start?state=<state>. It is the
// session producer for the standalone consent flow (#332): the callback
// mandates a matching forge_session, and this is where the browser gets it.
// The step is necessary because the user's browser only ever touches Forge at
// the callback (after the IdP), so the cookie must be planted on an earlier
// same-origin visit — this redirect.
//
// The session value lives ONLY in the server-side binding and the cookie, never
// in a URL query, so it is never leaked to the IdP via Referer (unlike the
// state, which round-trips through the IdP by design). SameSite=Lax lets the
// cookie survive the top-level cross-site redirect back from the IdP.
//
// Scope of the guarantee (see docs/mcp/configuration.md — Trust model): the
// browser here is ANONYMOUS, and this plants the cookie for whoever holds the
// link. So the session binding proves browser CONTINUITY across the round-trip
// (a stolen state alone, replayed from a different browser, can't complete) —
// it does NOT prove the completing user is the parked subject. The link is a
// bearer capability; its real containment is authenticated-channel delivery +
// single-use short-TTL state.
func makeMCPStartHandler(binder *stateBinder) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		state := req.URL.Query().Get("state")
		b, ok := binder.Peek(state) // Peek, not Consume — the state is spent at the callback.
		if !ok || b.authorizeURL == "" || b.session == "" {
			http.Error(w, "invalid or expired consent link", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     forgeSessionCookie,
			Value:    b.session,
			Path:     "/mcp/oauth/",
			HttpOnly: true,
			Secure:   req.TLS != nil || strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https"),
			SameSite: http.SameSiteLaxMode,
			Expires:  b.deadline,
		})
		http.Redirect(w, req, b.authorizeURL, http.StatusFound)
	}
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
		// Session-continuity guard — MANDATORY. This endpoint is unauthenticated
		// (a browser redirect carries no bearer token) and is registered on
		// the network-exposed main server (cfg.Host, possibly 0.0.0.0), so
		// the state binding is its ENTIRE security. Single-use + expiry alone
		// would let anyone who obtains a state within its TTL complete the
		// flow from a DIFFERENT browser, so we additionally require the callback
		// to land in the SAME browser that started it (the forge_session cookie
		// planted at /start). An empty bound session is a config/Issue-side bug
		// (a network-exposed flow must bind a session) and is rejected
		// fail-closed rather than silently downgrading to single-use+expiry.
		//
		// What this does NOT guarantee (see docs/mcp/configuration.md — Trust
		// model): in standalone mode the browser is anonymous, so this proves
		// browser continuity across the IdP round-trip, NOT that the completing
		// user is b.subject. Containment against a leaked link is
		// authenticated-channel delivery + the short single-use TTL, not this
		// cookie. A future loopback-bound variant would bind the listener to
		// localhost; the tamper-proof alternative verifies IdP userinfo == subject.
		if b.session == "" || sessionOf(req) != b.session {
			http.Error(w, "state/session mismatch", http.StatusBadRequest)
			return
		}
		// Exchange the code for a token and store it for {subject, server}.
		// Only AFTER this succeeds do we resume — resolving the gate with no
		// grant would just re-park the call (delegation follows
		// authorization: never resume before the grant exists). The PKCE
		// verifier bound to the state proves this is the same flow we started.
		if err := complete(req.Context(), b.subject, b.server, code, b.verifier); err != nil {
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
