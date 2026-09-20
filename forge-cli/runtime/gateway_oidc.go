package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
)

// Native OIDC gateway credential (#455 slice 2). An alternative to the
// api_key_helper external command (slice 1): forge does the OAuth/OIDC flow
// itself and caches/refreshes the token in the same oauth store, keyed by the
// OIDC config identity. Two grants: client_credentials (headless — mint, no
// browser) and auth_code (interactive browser login + refresh-token refresh).
//
// Guardrail (#455): the OIDC flow must never target the anthropic PUBLIC API
// (api.anthropic.com) — that API is x-api-key, not OAuth. A gateway fronting
// anthropic behind an IdP is fine; signing in against anthropic's own URL is
// refused.

// oidcDiscoveryTimeout bounds the well-known discovery GET.
const oidcDiscoveryTimeout = 30 * time.Second

// oidcIdentity is the stable identity of an OIDC gateway credential, for cache
// keying — grant + endpoints + client + sorted scopes. Distinct configs get
// distinct cached tokens.
func oidcIdentity(o *settings.ModelGatewayOIDC) string {
	scopes := append([]string(nil), o.Scopes...)
	sort.Strings(scopes)
	return strings.Join([]string{
		o.Grant, o.Issuer, o.AuthorizeURL, o.TokenURL, o.ClientID, strings.Join(scopes, " "),
	}, "\x00")
}

// GatewayOIDCCredKey is the oauth-store key for an OIDC gateway token.
func GatewayOIDCCredKey(o *settings.ModelGatewayOIDC) string {
	sum := sha256.Sum256([]byte(oidcIdentity(o)))
	return "gateway-oidc-" + hex.EncodeToString(sum[:])[:16]
}

// CachedGatewayOIDCToken returns the stored token WITHOUT acquiring one.
func CachedGatewayOIDCToken(o *settings.ModelGatewayOIDC) (*oauth.Token, error) {
	return oauth.LoadCredentials(GatewayOIDCCredKey(o))
}

// ClearGatewayOIDCToken deletes the cached token (`forge auth logout`).
func ClearGatewayOIDCToken(o *settings.ModelGatewayOIDC) error {
	return oauth.DeleteCredentials(GatewayOIDCCredKey(o))
}

// EnsureGatewayOIDCToken is the OVERLAY path: return the cached token when
// valid; else refresh (auth_code with a refresh token) or mint
// (client_credentials, headless). It never opens a browser — interactive
// auth_code acquisition is LoginGatewayOIDC, run by `forge auth login`.
func EnsureGatewayOIDCToken(ctx context.Context, o *settings.ModelGatewayOIDC) (*oauth.Token, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	key := GatewayOIDCCredKey(o)

	if tok, err := oauth.LoadCredentials(key); err == nil && tok != nil && tok.AccessToken != "" {
		if !tok.IsExpiredWithBuffer(gatewayRefreshBuffer) {
			return tok, nil
		}
		// Expired. Refresh without a browser when we hold a refresh token.
		if tok.RefreshToken != "" {
			if _, tokenURL, derr := resolveOIDCEndpoints(ctx, o); derr == nil && oidcAnthropicGuard(o.Issuer, "", tokenURL) == nil {
				if refreshed, rerr := oauth.RefreshTokenCtx(ctx, nil, tokenURL, o.ClientID, tok.RefreshToken); rerr == nil && refreshed.AccessToken != "" {
					if refreshed.RefreshToken == "" {
						refreshed.RefreshToken = tok.RefreshToken // IdP may not re-issue one
					}
					ensureOIDCExpiry(refreshed)
					if serr := oauth.SaveCredentials(key, refreshed); serr != nil {
						return nil, fmt.Errorf("caching refreshed gateway token: %w", serr)
					}
					return refreshed, nil
				}
			}
		}
	}

	if o.Grant == "client_credentials" {
		return mintClientCredentials(ctx, o, key)
	}
	// auth_code with no valid/refreshable cached token: the overlay must not
	// pop a browser mid-run — the developer logs in explicitly.
	return nil, fmt.Errorf("no valid cached gateway token for the auth_code grant; run 'forge auth login'")
}

// LoginGatewayOIDC is the INTERACTIVE path (`forge auth login`): auth_code runs
// the browser flow; client_credentials mints a token.
func LoginGatewayOIDC(ctx context.Context, o *settings.ModelGatewayOIDC) (*oauth.Token, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	key := GatewayOIDCCredKey(o)

	if o.Grant == "client_credentials" {
		return mintClientCredentials(ctx, o, key)
	}

	// auth_code: interactive browser + configurable loopback callback.
	authorizeURL, tokenURL, err := resolveOIDCEndpoints(ctx, o)
	if err != nil {
		return nil, err
	}
	if err := oidcAnthropicGuard(o.Issuer, authorizeURL, tokenURL); err != nil {
		return nil, err
	}
	redirect := o.RedirectURI
	if redirect == "" {
		redirect = oidcDefaultRedirect
	}
	return runOIDCAuthCodeFlow(ctx, o, authorizeURL, tokenURL, redirect, key)
}

// oidcDefaultRedirect is the loopback callback used when the config sets none.
const oidcDefaultRedirect = "http://localhost:1455/auth/callback"

// oidcLoginTimeout bounds the interactive auth_code flow (browser login).
const oidcLoginTimeout = 3 * time.Minute

// oidcBrowserOpener opens a URL in the default browser. A package var so tests
// can inject a callback simulator.
var oidcBrowserOpener = openBrowserURL

// runOIDCAuthCodeFlow runs auth-code + PKCE against a CONFIGURABLE loopback
// redirect (so it matches whatever the IdP client already has registered), then
// caches the token under key. Reuses oauth's PKCE/state/exchange primitives.
func runOIDCAuthCodeFlow(ctx context.Context, o *settings.ModelGatewayOIDC, authorizeURL, tokenURL, redirectURI, key string) (*oauth.Token, error) {
	ru, err := url.Parse(redirectURI)
	if err != nil || ru.Host == "" {
		return nil, fmt.Errorf("gateway oidc: invalid redirect_uri %q", redirectURI)
	}
	if !strings.EqualFold(ru.Scheme, "http") || !isLoopbackHost(ru.Hostname()) {
		return nil, fmt.Errorf("gateway oidc: redirect_uri must be an http loopback (localhost/127.0.0.1/[::1]), got %q", redirectURI)
	}
	callbackPath := ru.Path
	if callbackPath == "" {
		callbackPath = "/"
	}

	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state, err := oauth.GenerateState()
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", ru.Host)
	if err != nil {
		return nil, fmt.Errorf("gateway oidc: cannot bind the redirect_uri %q (is the port free / registered?): %w", redirectURI, err)
	}

	type cbResult struct {
		code, state, errStr string
	}
	resultCh := make(chan cbResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := cbResult{code: q.Get("code"), state: q.Get("state"), errStr: q.Get("error")}
		w.Header().Set("Content-Type", "text/html")
		if res.errStr != "" || res.code == "" {
			_, _ = io.WriteString(w, "<html><body><h2>Login failed</h2><p>You can close this tab and return to the terminal.</p></body></html>")
		} else {
			_, _ = io.WriteString(w, "<html><body><h2>Login successful</h2><p>You can close this tab and return to the terminal.</p></body></html>")
		}
		select {
		case resultCh <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	authURL := buildOIDCAuthorizeURL(authorizeURL, o.ClientID, redirectURI, o.Scopes, state, pkce.Challenge)
	if berr := oidcBrowserOpener(authURL); berr != nil {
		fmt.Fprintf(os.Stderr, "Open this URL to sign in:\n%s\n", authURL)
	}

	waitCtx, cancel := context.WithTimeout(ctx, oidcLoginTimeout)
	defer cancel()
	select {
	case res := <-resultCh:
		if res.errStr != "" {
			return nil, fmt.Errorf("gateway oidc: IdP returned error %q", res.errStr)
		}
		if res.state != state {
			return nil, fmt.Errorf("gateway oidc: state mismatch (CSRF check failed)")
		}
		tok, err := oauth.ExchangeCodeCtx(waitCtx, nil, tokenURL, o.ClientID, res.code, redirectURI, pkce.Verifier)
		if err != nil {
			return nil, err
		}
		ensureOIDCExpiry(tok)
		if err := oauth.SaveCredentials(key, tok); err != nil {
			return nil, fmt.Errorf("caching gateway token: %w", err)
		}
		return tok, nil
	case <-waitCtx.Done():
		return nil, fmt.Errorf("gateway oidc: timed out waiting for the browser login")
	}
}

// buildOIDCAuthorizeURL builds the auth-code + PKCE authorize URL.
func buildOIDCAuthorizeURL(authorizeURL, clientID, redirectURI string, scopes []string, state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(authorizeURL, "?") {
		sep = "&"
	}
	return authorizeURL + sep + q.Encode()
}

// openBrowserURL opens a URL in the platform default browser.
func openBrowserURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func mintClientCredentials(ctx context.Context, o *settings.ModelGatewayOIDC, key string) (*oauth.Token, error) {
	_, tokenURL, err := resolveOIDCEndpoints(ctx, o)
	if err != nil {
		return nil, err
	}
	if err := oidcAnthropicGuard(o.Issuer, "", tokenURL); err != nil {
		return nil, err
	}
	secret := os.Getenv(o.ClientSecretEnv)
	if secret == "" {
		return nil, fmt.Errorf("gateway oidc: client_secret env %q is empty", o.ClientSecretEnv)
	}
	tok, err := oauth.ClientCredentialsTokenCtx(ctx, nil, tokenURL, o.ClientID, secret, o.Scopes)
	if err != nil {
		return nil, err
	}
	ensureOIDCExpiry(tok)
	if err := oauth.SaveCredentials(key, tok); err != nil {
		return nil, fmt.Errorf("caching gateway token: %w", err)
	}
	return tok, nil
}

// ensureOIDCExpiry backfills ExpiresAt from the token's JWT exp when the token
// response carried no expires_in (else IsExpiredWithBuffer treats it as always
// expired → a re-mint every call).
func ensureOIDCExpiry(tok *oauth.Token) {
	if tok.ExpiresAt.IsZero() {
		if exp, ok := oauth.ParseJWTExp(tok.AccessToken); ok {
			tok.ExpiresAt = exp
		}
	}
}

// resolveOIDCEndpoints returns the authorize + token URLs, discovering them from
// the issuer's /.well-known/openid-configuration when not set explicitly.
func resolveOIDCEndpoints(ctx context.Context, o *settings.ModelGatewayOIDC) (authorizeURL, tokenURL string, err error) {
	authorizeURL, tokenURL = o.AuthorizeURL, o.TokenURL
	if (authorizeURL == "" || tokenURL == "") && o.Issuer != "" {
		a, t, derr := discoverOIDC(ctx, o.Issuer)
		if derr != nil {
			return "", "", derr
		}
		if authorizeURL == "" {
			authorizeURL = a
		}
		if tokenURL == "" {
			tokenURL = t
		}
	}
	if tokenURL == "" {
		return "", "", fmt.Errorf("gateway oidc: no token endpoint (set token_url or issuer)")
	}
	return authorizeURL, tokenURL, nil
}

// discoverOIDC fetches <issuer>/.well-known/openid-configuration (RFC 8414 /
// OIDC Discovery) and returns the authorization + token endpoints.
func discoverOIDC(ctx context.Context, issuer string) (authorizeURL, tokenURL string, err error) {
	// RFC 8414 mandates an https issuer; reject a cleartext/other scheme so a
	// misconfigured or MITM-able discovery endpoint can't be reached. Loopback
	// (http://localhost) is allowed for local dev IdPs.
	if u, perr := url.Parse(issuer); perr != nil {
		return "", "", fmt.Errorf("oidc issuer is not a valid URL: %q", issuer)
	} else if !strings.EqualFold(u.Scheme, "https") && !isLoopbackHost(u.Hostname()) {
		return "", "", fmt.Errorf("oidc issuer must be an https URL (RFC 8414), got %q", issuer)
	}
	wk := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wk, nil)
	if err != nil {
		return "", "", err
	}
	client := &http.Client{Timeout: oidcDiscoveryTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("oidc discovery (%s): %w", wk, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("oidc discovery (%s): status %d", wk, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("oidc discovery (%s): %w", wk, err)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", "", fmt.Errorf("oidc discovery (%s): parse: %w", wk, err)
	}
	return doc.AuthorizationEndpoint, doc.TokenEndpoint, nil
}

// isLoopbackHost reports whether host is a loopback address (allowed to use
// http for a local dev IdP).
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// oidcAnthropicGuard refuses an OIDC flow whose issuer / authorize / token host
// is the anthropic PUBLIC API — that API uses x-api-key, not OAuth (#455
// guardrail). An IdP-fronted anthropic gateway (a different host) is allowed.
func oidcAnthropicGuard(urls ...string) error {
	for _, raw := range urls {
		if raw == "" {
			continue
		}
		if u, perr := url.Parse(raw); perr == nil {
			if strings.EqualFold(u.Hostname(), "api.anthropic.com") {
				return fmt.Errorf("gateway oidc: refusing to run an OAuth flow against the anthropic public URL (%s) — it authenticates with x-api-key, not OAuth", u.Hostname())
			}
		}
	}
	return nil
}
