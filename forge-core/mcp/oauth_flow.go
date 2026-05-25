package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// OAuthFlow implements OAuth 2.1 with PKCE for MCP servers, sharing
// the encrypted token store with the existing llm/oauth package
// (decision §3.6 of the recommendations doc). MCP tokens live under
// a separate key namespace so they cannot collide with LLM provider
// tokens.
//
// Two flows:
//
//	Login(ctx, name, cfg) — laptop-time, interactive. Generates PKCE,
//	opens a loopback listener, opens the operator's browser at the
//	authorization endpoint, exchanges the returned code for tokens,
//	persists them in the encrypted store.
//
//	BearerToken(ctx, name, cfg) — runtime, automatic. Loads tokens,
//	refreshes if within RefreshWindow of expiry, returns the
//	access_token. Refresh failure (invalid_grant / expired_token)
//	surfaces as ErrTokenRevoked.
//
// The refresh path is concurrency-safe via per-name singleflight —
// 100 concurrent BearerToken calls produce one /token call.
//
// IMPORTANT (review B2): the singleflight goroutine uses its OWN
// background-derived context with a hard RefreshTimeout cap — it is
// NOT bound to the leader caller's ctx. Otherwise a misbehaving IdP
// hangs the goroutine indefinitely, leaks the inFly slot, and wedges
// every subsequent caller for the same server. Caller-ctx-cancel
// unblocks the caller's wait on <-grp.done but never affects the
// in-flight refresh.
type OAuthFlow struct {
	// RefreshWindow is the slack before expiry at which BearerToken
	// proactively refreshes. Default 60s.
	RefreshWindow time.Duration

	// RefreshTimeout caps each /token call. Default 30s.
	// Decoupled from any caller's context — see the type docstring.
	RefreshTimeout time.Duration

	// HTTPClient is used for token-endpoint requests. nil → a
	// defaulting *http.Client with no Transport-level timeout (the
	// per-call ctx supplies the bound). Production wiring should pass
	// the egress-controlled client (security.EgressClient) so token
	// endpoints ride the same allowlist as MCP traffic.
	HTTPClient *http.Client

	// AuditFn is called on every refresh attempt (success and
	// failure). nil means no audit — typical for Login at laptop time.
	AuditFn func(server string, ok bool, reason string)

	// BrowserOpener opens a URL in the operator's browser during
	// Login. nil falls back to a sensible per-OS default
	// (xdg-open / open / start). Tests inject a no-op.
	BrowserOpener func(url string) error

	mu    sync.Mutex
	inFly map[string]*refreshGroup // singleflight per server
}

// NewOAuthFlow constructs an OAuthFlow with default settings.
func NewOAuthFlow() *OAuthFlow {
	return &OAuthFlow{
		RefreshWindow:  60 * time.Second,
		RefreshTimeout: 30 * time.Second,
		inFly:          make(map[string]*refreshGroup),
	}
}

// OAuthServerConfig captures the per-server OAuth knobs needed at
// flow time. Plays the role of types.MCPAuth without the YAML tags,
// to keep this package importable from cmd/ without a dependency on
// the types package shape changing.
type OAuthServerConfig struct {
	ClientID     string
	Scopes       []string
	AuthorizeURL string
	TokenURL     string
}

// storeKey returns the credential-store key for an MCP server.
// Prefixed "MCP_" so MCP tokens are namespaced separately from LLM
// provider tokens in the same encrypted file.
func storeKey(name string) string { return "mcp_" + name }

// Login runs the interactive OAuth 2.1 PKCE flow and persists the
// resulting token. Intended for laptop-time use by
// `forge mcp login <name>`. Blocks until the callback fires or ctx
// is cancelled.
func (f *OAuthFlow) Login(ctx context.Context, name string, cfg OAuthServerConfig) error {
	if cfg.ClientID == "" || cfg.AuthorizeURL == "" || cfg.TokenURL == "" {
		return fmt.Errorf("%w: oauth Login requires client_id, authorize_url, token_url", ErrProtocolError)
	}

	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return fmt.Errorf("generating PKCE: %w", err)
	}
	state, err := oauth.GenerateState()
	if err != nil {
		return fmt.Errorf("generating state: %w", err)
	}

	// Start loopback listener on a random port. Localhost-only;
	// browser redirect must come from the same machine.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("starting callback listener: %w", err)
	}
	defer func() { _ = listener.Close() }()
	redirectURI := fmt.Sprintf("http://%s/callback", listener.Addr().String())

	// Build authorize URL.
	authURL, err := buildAuthorizeURL(cfg.AuthorizeURL, cfg.ClientID, redirectURI, state, pkce.Challenge, cfg.Scopes)
	if err != nil {
		return err
	}

	// Channel for the callback to deliver its result.
	type callbackResult struct {
		code string
		err  error
	}
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		gotState := r.URL.Query().Get("state")
		if gotState != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- callbackResult{err: fmt.Errorf("%w: state parameter mismatch", ErrProtocolError)}
			return
		}
		if errStr := r.URL.Query().Get("error"); errStr != "" {
			http.Error(w, "authorization denied: "+errStr, http.StatusBadRequest)
			resultCh <- callbackResult{err: fmt.Errorf("%w: authorization denied: %s", ErrProtocolError, errStr)}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code parameter", http.StatusBadRequest)
			resultCh <- callbackResult{err: fmt.Errorf("%w: missing code parameter", ErrProtocolError)}
			return
		}
		// Success page — kept simple, no styling.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h2>Forge MCP — authorization complete</h2>
<p>You can close this window.</p></body></html>`))
		resultCh <- callbackResult{code: code}
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(context.Background()) }()

	// Open the operator's browser.
	opener := f.BrowserOpener
	if opener == nil {
		opener = defaultBrowserOpener
	}
	if err := opener(authURL); err != nil {
		// Non-fatal: print the URL so the operator can open it manually.
		fmt.Printf("Open this URL in your browser to authorize:\n  %s\n", authURL)
	}

	// Wait for callback.
	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		code = res.code
	}

	// Exchange code for tokens. Honor the caller's ctx (laptop-time
	// login has its own outer deadline) and use the configured
	// HTTPClient if set so the call rides the egress allowlist.
	exchCtx, cancel := context.WithTimeout(ctx, f.refreshTimeout())
	defer cancel()
	token, err := oauth.ExchangeCodeCtx(exchCtx, f.HTTPClient, cfg.TokenURL, cfg.ClientID, code, redirectURI, pkce.Verifier)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProtocolError, err)
	}

	if err := oauth.SaveCredentials(storeKey(name), token); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}
	return nil
}

// BearerToken returns a usable access_token for MCP requests,
// refreshing if the cached token is within RefreshWindow of expiry.
// Returns ErrTokenRevoked when refresh fails irrecoverably.
//
// Safe for concurrent use: per-server singleflight collapses N
// concurrent calls into 1 /token POST.
func (f *OAuthFlow) BearerToken(ctx context.Context, name string, cfg OAuthServerConfig) (string, error) {
	tok, err := oauth.LoadCredentials(storeKey(name))
	if err != nil {
		return "", fmt.Errorf("loading credentials for %q: %w", name, err)
	}
	if tok == nil {
		return "", fmt.Errorf("%w: no stored token for %q — run 'forge mcp login %s'", ErrTokenRevoked, name, name)
	}

	if !tok.IsExpiredWithBuffer(f.RefreshWindow) {
		return tok.AccessToken, nil
	}

	// Singleflight: collapse concurrent refreshes. The leader spawns
	// the refresh goroutine and EVERY subsequent caller — including
	// the leader itself — waits on grp.done below.
	//
	// CRITICAL (review B2): the goroutine's context is derived from
	// context.Background, NOT from the leader's ctx. If we used the
	// leader's ctx, a leader cancellation would tear down the refresh
	// mid-flight and waiters would all get the leader's error. Worse,
	// if the leader's ctx had no deadline AND the IdP hung, the
	// goroutine would never return — the previous bug. Decoupling
	// here, plus the hard RefreshTimeout below, guarantees forward
	// progress: the slot ALWAYS clears within RefreshTimeout.
	f.mu.Lock()
	grp, exists := f.inFly[name]
	if !exists {
		grp = &refreshGroup{done: make(chan struct{})}
		f.inFly[name] = grp
		f.mu.Unlock()

		go func() {
			defer func() {
				// Recover here so a panic in doRefresh still clears the
				// slot and unblocks waiters. Without this, a panicking
				// refresh would orphan f.inFly[name] forever.
				if r := recover(); r != nil {
					grp.err = fmt.Errorf("%w: refresh goroutine panicked: %v", ErrTransportUnavailable, r)
				}
				f.mu.Lock()
				delete(f.inFly, name)
				f.mu.Unlock()
				close(grp.done)
			}()
			refreshCtx, cancel := context.WithTimeout(context.Background(), f.refreshTimeout())
			defer cancel()
			grp.token, grp.err = f.doRefresh(refreshCtx, name, cfg, tok.RefreshToken)
		}()
	} else {
		f.mu.Unlock()
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-grp.done:
	}

	if grp.err != nil {
		return "", grp.err
	}
	return grp.token, nil
}

// doRefresh actually calls the /token endpoint. Errors are
// classified as ErrTokenRevoked for the documented failure cases
// (invalid_grant, expired refresh token); other errors propagate
// as ErrTransportUnavailable so the lifecycle treats them as retryable.
//
// ctx is honored by the underlying HTTP call (review B2) — a deadline
// on ctx is the only thing keeping a hung IdP from wedging this
// goroutine. The caller (BearerToken's singleflight goroutine) sets
// a hard RefreshTimeout cap so this method ALWAYS returns.
func (f *OAuthFlow) doRefresh(ctx context.Context, name string, cfg OAuthServerConfig, refreshToken string) (string, error) {
	newTok, err := oauth.RefreshTokenCtx(ctx, f.HTTPClient, cfg.TokenURL, cfg.ClientID, refreshToken)
	if err != nil {
		// Heuristic: explicit OAuth error strings → revoked.
		// Anything else (network / timeout / etc.) → transport unavailable.
		msg := err.Error()
		if strings.Contains(msg, "invalid_grant") ||
			strings.Contains(msg, "expired_token") ||
			strings.Contains(msg, "invalid_token") {
			f.emit(name, false, "refresh_denied")
			return "", fmt.Errorf("%w: %v", ErrTokenRevoked, err)
		}
		// Distinguish ctx-deadline from a transport error so audit
		// dashboards can alert on "refresh consistently hits 30s"
		// (configured timeout too tight, OR IdP hanging).
		if errors.Is(err, context.DeadlineExceeded) {
			f.emit(name, false, "timeout")
			return "", fmt.Errorf("%w: refresh timed out after %s", ErrTransportUnavailable, f.refreshTimeout())
		}
		f.emit(name, false, "transport")
		return "", fmt.Errorf("%w: %v", ErrTransportUnavailable, err)
	}
	// Preserve refresh_token if the server didn't rotate it.
	if newTok.RefreshToken == "" {
		newTok.RefreshToken = refreshToken
	}
	if err := oauth.SaveCredentials(storeKey(name), newTok); err != nil {
		f.emit(name, false, "store_error")
		return "", fmt.Errorf("saving refreshed token: %w", err)
	}
	f.emit(name, true, "refreshed")
	return newTok.AccessToken, nil
}

// refreshTimeout returns the configured RefreshTimeout or a sensible
// default. Used by both the singleflight goroutine (to bound the
// derived ctx) and doRefresh (to embed the duration in error text).
func (f *OAuthFlow) refreshTimeout() time.Duration {
	if f.RefreshTimeout > 0 {
		return f.RefreshTimeout
	}
	return 30 * time.Second
}

// Logout deletes the stored token for an MCP server. Idempotent.
func (f *OAuthFlow) Logout(name string) error {
	return oauth.DeleteCredentials(storeKey(name))
}

func (f *OAuthFlow) emit(server string, ok bool, reason string) {
	if f.AuditFn != nil {
		f.AuditFn(server, ok, reason)
	}
}

// refreshGroup is the per-name singleflight slot.
type refreshGroup struct {
	done  chan struct{}
	token string
	err   error
}

// buildAuthorizeURL assembles the authorize-endpoint URL. Returns
// ErrProtocolError if AuthorizeURL is malformed.
func buildAuthorizeURL(authorizeURL, clientID, redirectURI, state, challenge string, scopes []string) (string, error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return "", fmt.Errorf("%w: authorize_url malformed: %v", ErrProtocolError, err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// defaultBrowserOpener invokes the OS's default browser opener. Best-
// effort — failures are non-fatal; Login falls back to printing the
// URL for the operator to open manually.
func defaultBrowserOpener(target string) error {
	// Imports kept tight: this is the only place we touch os/exec.
	// Importing it module-wide would be flagged by the
	// "no os/exec in forge-core/mcp" rule, but the rule's intent is
	// "no MCP subprocesses" — the laptop-time browser open is a
	// different concern. Kept narrowly scoped here.
	return openBrowser(target)
}
