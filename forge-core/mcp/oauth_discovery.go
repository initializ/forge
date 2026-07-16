package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

const (
	// defaultDiscoveryTimeout bounds a discovery/DCR request when the
	// flow has no egress-controlled client injected (laptop login).
	defaultDiscoveryTimeout = 15 * time.Second
	// maxMetadataBytes caps a metadata / registration response so a
	// hostile or misconfigured endpoint can't stream unbounded data.
	maxMetadataBytes = 1 << 20 // 1 MiB
)

// MCP authorization discovery + dynamic client registration (#316).
//
// Implements the MCP Authorization spec's zero-config path so that a
// server declared with only `transport: http` + `url:` can complete
// OAuth with NO client_id / authorize_url / token_url in forge.yaml:
//
//	RFC 9728  — protected-resource metadata: which authorization
//	            server(s) govern this MCP resource.
//	RFC 8414  — authorization-server metadata: discover the
//	            authorize / token / registration endpoints.
//	RFC 7591  — dynamic client registration: mint a client_id at
//	            first login; persisted so it is reused, never re-minted.
//
// The resolved endpoints + minted client are persisted in the same
// encrypted store as the token (oauth.SaveRecord) under regStoreKey,
// so runtime refresh and a pod restart reuse them without re-running
// discovery. Explicit config always wins (§ resolveOAuthConfig).

// regStoreKey returns the credential-store key for a server's OAuth
// registration record. Namespaced apart from the token key.
func regStoreKey(name string) string { return "mcp_reg_" + name }

// oauthRegistration is the persisted result of discovery + DCR. It
// carries everything needed to run the authorize/refresh flow later
// without re-discovering: the resolved endpoints and the minted client.
type oauthRegistration struct {
	AuthorizeURL    string   `json:"authorize_url"`
	TokenURL        string   `json:"token_url"`
	RegistrationURL string   `json:"registration_url,omitempty"`
	ClientID        string   `json:"client_id"`
	Scopes          []string `json:"scopes,omitempty"`
	// NOTE: a DCR client_secret is deliberately NOT persisted (#320
	// review, finding 1). We register as a public PKCE client and the
	// token path sends no secret, so a secret would be stored (possibly
	// in the plaintext-fallback store) for no benefit — pure liability.
	// A confidential client is refused at resolve time instead.
}

// loopbackRedirectURIs are what DCR registers for laptop-time login.
// RFC 8252 §7.3: an AS MUST allow a variable port for loopback
// redirect URIs, so we register the port-less loopback so the minted
// client is reusable across logins (each picks a fresh ephemeral port).
var loopbackRedirectURIs = []string{
	"http://127.0.0.1/callback",
	"http://localhost/callback",
}

// authServerMetadata is the subset of RFC 8414 / OpenID discovery we consume.
type authServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

// protectedResourceMetadata is the subset of RFC 9728 we consume.
type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// resolveOAuthConfig fills in whatever the operator did not configure,
// in strict precedence order:
//
//  1. Fully explicit config (client_id + authorize_url + token_url) → used verbatim.
//  2. A persisted registration from a prior discovery → reused (no network).
//  3. allowDiscovery → run RFC 9728 → 8414 → 7591 and persist the result.
//
// allowDiscovery is true only for interactive Login. BearerToken
// (refresh) passes false: if steps 1–2 can't satisfy it, the operator
// must `forge mcp login` first, exactly as before — a refresh path
// never mints a client.
func (f *OAuthFlow) resolveOAuthConfig(ctx context.Context, name string, cfg OAuthServerConfig, allowDiscovery bool) (OAuthServerConfig, error) {
	// 1. Fully explicit — static override always wins.
	if cfg.ClientID != "" && cfg.AuthorizeURL != "" && cfg.TokenURL != "" {
		return cfg, nil
	}

	// 2. Persisted registration from a prior discovery/login.
	var reg oauthRegistration
	found, err := oauth.LoadRecord(regStoreKey(name), &reg)
	if err != nil {
		return cfg, fmt.Errorf("reading oauth registration for %q: %w", name, err)
	}
	if found {
		return mergeRegistration(cfg, reg), nil
	}

	// Fail-closed asymmetry (#320 review, finding 3): the refresh path
	// (BearerToken → allowDiscovery=false) stops here. So a
	// partially-materialized config in a fresh pod — e.g. a platform env
	// var came through empty, so step 1 didn't match and no record
	// exists — fails closed and NEVER mints a divergent client. Only the
	// interactive login path (allowDiscovery=true) falls through to DCR.
	if !allowDiscovery {
		return cfg, fmt.Errorf("%w: no oauth endpoints for %q and no stored registration — run 'forge mcp login %s'", ErrNoToken, name, name)
	}

	// 3. Discover + register.
	if cfg.ServerURL == "" {
		return cfg, fmt.Errorf("%w: server %q has no oauth endpoints configured and no url to discover from", ErrProtocolError, name)
	}
	meta, err := f.discoverAuthServer(ctx, cfg.ServerURL)
	if err != nil {
		return cfg, err
	}

	clientID := cfg.ClientID
	if clientID == "" {
		if meta.RegistrationEndpoint == "" {
			return cfg, fmt.Errorf("%w: server %q advertises no registration_endpoint and no client_id is configured — supply client_id/authorize_url/token_url, or use a server that supports dynamic client registration", ErrProtocolError, name)
		}
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = meta.ScopesSupported
		}
		var clientSecret string
		clientID, clientSecret, err = f.registerClient(ctx, meta.RegistrationEndpoint, scopes)
		if err != nil {
			return cfg, err
		}
		if clientSecret != "" {
			// We register as a public PKCE client (token_endpoint_auth_method
			// = none) and the token path sends no client_secret. If the AS
			// ignored that and issued a confidential client, we can't
			// authenticate it — and we refuse to persist a secret (#320
			// finding 1). Fail closed rather than half-register.
			return cfg, fmt.Errorf("%w: server %q issued a confidential client (client_secret); Forge supports only public PKCE clients for MCP OAuth — configure client_id/authorize_url/token_url explicitly for this server", ErrProtocolError, name)
		}
	}

	reg = oauthRegistration{
		AuthorizeURL:    firstNonEmpty(cfg.AuthorizeURL, meta.AuthorizationEndpoint),
		TokenURL:        firstNonEmpty(cfg.TokenURL, meta.TokenEndpoint),
		RegistrationURL: meta.RegistrationEndpoint,
		ClientID:        clientID,
		Scopes:          cfg.Scopes,
	}
	if reg.AuthorizeURL == "" || reg.TokenURL == "" {
		return cfg, fmt.Errorf("%w: discovery for %q did not yield both authorize and token endpoints", ErrProtocolError, name)
	}
	if err := oauth.SaveRecord(regStoreKey(name), &reg); err != nil {
		return cfg, fmt.Errorf("persisting oauth registration for %q: %w", name, err)
	}
	return mergeRegistration(cfg, reg), nil
}

// mergeRegistration fills empty cfg fields from a persisted/discovered
// registration. Explicit cfg fields are never overwritten (precedence).
func mergeRegistration(cfg OAuthServerConfig, reg oauthRegistration) OAuthServerConfig {
	cfg.ClientID = firstNonEmpty(cfg.ClientID, reg.ClientID)
	cfg.AuthorizeURL = firstNonEmpty(cfg.AuthorizeURL, reg.AuthorizeURL)
	cfg.TokenURL = firstNonEmpty(cfg.TokenURL, reg.TokenURL)
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = reg.Scopes
	}
	return cfg
}

// discoverAuthServer runs RFC 9728 → RFC 8414 from the MCP server URL
// and returns the authorization-server metadata.
//
// SECURITY (#320 review, minor): the RFC 9728 result — the
// `resource_metadata` pointer and the authorization_servers list — is
// server-controlled, and these fetches run with the plain 15s client at
// laptop-time login (the runtime/refresh path IS egress-enforced). A
// hostile MCP url could thus steer the login-time fetch at an internal
// metadata endpoint. Low severity (operator-initiated, laptop-side,
// byte/timeout-bounded), noted so it isn't mistaken for egress-enforced.
func (f *OAuthFlow) discoverAuthServer(ctx context.Context, serverURL string) (authServerMetadata, error) {
	var zero authServerMetadata

	authServers, err := f.discoverProtectedResource(ctx, serverURL)
	if err != nil {
		return zero, err
	}

	// RFC 9728 advertises a LIST of authorization servers; try each in
	// order so a multi-AS resource with a dead primary still completes.
	// RFC 8414: {issuer}/.well-known/oauth-authorization-server, with the
	// OpenID variant as a fallback for servers that only publish
	// openid-configuration.
	for _, asURL := range authServers {
		for _, wk := range []string{
			wellKnown(asURL, "oauth-authorization-server"),
			wellKnown(asURL, "openid-configuration"),
		} {
			meta, err := fetchJSON[authServerMetadata](ctx, f.httpClient(), wk)
			if err == nil && meta.TokenEndpoint != "" && meta.AuthorizationEndpoint != "" {
				return meta, nil
			}
		}
	}
	return zero, fmt.Errorf("%w: no usable authorization-server metadata for %s (tried %d server(s))", ErrProtocolError, serverURL, len(authServers))
}

// discoverProtectedResource resolves the authorization server(s) for an
// MCP resource (RFC 9728), returning the full authorization_servers
// list. It first tries the well-known path derived from the server URL;
// if the server instead answers 401 with a WWW-Authenticate
// `resource_metadata` pointer, that is honored too.
func (f *OAuthFlow) discoverProtectedResource(ctx context.Context, serverURL string) ([]string, error) {
	// Primary: the well-known path off the server origin.
	prURL := wellKnown(serverURL, "oauth-protected-resource")
	if pr, err := fetchJSON[protectedResourceMetadata](ctx, f.httpClient(), prURL); err == nil && len(pr.AuthorizationServers) > 0 {
		return pr.AuthorizationServers, nil
	}

	// Fallback: probe the server itself and read the 401
	// WWW-Authenticate `resource_metadata` param (RFC 9728 §5.1).
	if rm := f.probeResourceMetadataURL(ctx, serverURL); rm != "" {
		if pr, err := fetchJSON[protectedResourceMetadata](ctx, f.httpClient(), rm); err == nil && len(pr.AuthorizationServers) > 0 {
			return pr.AuthorizationServers, nil
		}
	}
	return nil, fmt.Errorf("%w: could not discover an authorization server for %s (no RFC 9728 protected-resource metadata)", ErrProtocolError, serverURL)
}

// probeResourceMetadataURL makes an unauthenticated request to the MCP
// server and extracts the `resource_metadata` URL from a 401's
// WWW-Authenticate header, if present.
func (f *OAuthFlow) probeResourceMetadataURL(ctx context.Context, serverURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return ""
	}
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		return ""
	}
	return resourceMetadataParam(resp.Header.Get("WWW-Authenticate"))
}

// resourceMetadataParam pulls the resource_metadata="..." value out of
// a WWW-Authenticate header. Tolerant of ordering/quoting/whitespace.
func resourceMetadataParam(header string) string {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		// Drop the leading scheme token (e.g. "Bearer") if present.
		if i := strings.IndexByte(part, ' '); i >= 0 && !strings.Contains(part[:i], "=") {
			part = strings.TrimSpace(part[i+1:])
		}
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "resource_metadata") {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// registerClient performs RFC 7591 dynamic client registration and
// returns the minted client_id and (if issued) client_secret.
func (f *OAuthFlow) registerClient(ctx context.Context, registrationURL string, scopes []string) (clientID, clientSecret string, err error) {
	body := map[string]any{
		"client_name":                "Forge MCP",
		"redirect_uris":              loopbackRedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none", // public client (PKCE)
	}
	if len(scopes) > 0 {
		body["scope"] = strings.Join(scopes, " ")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationURL, strings.NewReader(string(raw)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: dynamic client registration request failed: %v", ErrTransportUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%w: dynamic client registration returned %d: %s", ErrProtocolError, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", fmt.Errorf("%w: parsing registration response: %v", ErrProtocolError, err)
	}
	if out.ClientID == "" {
		return "", "", fmt.Errorf("%w: registration response carried no client_id", ErrProtocolError)
	}
	return out.ClientID, out.ClientSecret, nil
}

// RegisteredOAuthHosts returns the authorize/token/registration hosts
// from persisted OAuth registrations for the named oauth servers. The
// runtime merges these into the egress allowlist — with discovery the
// auth-server host is not in forge.yaml to pre-seed, so it is learned
// from the login-time registration record (#316).
//
// Best-effort: a server with no stored registration (explicit config,
// or not yet logged in) contributes nothing here; explicit hosts come
// from security.MCPDomains as before. Callers must apply any
// credentials-dir override (oauth.SetCredentialsDir) first, so the
// right store is read.
func RegisteredOAuthHosts(oauthServerNames []string) []string {
	seen := map[string]struct{}{}
	for _, name := range oauthServerNames {
		var reg oauthRegistration
		found, err := oauth.LoadRecord(regStoreKey(name), &reg)
		if err != nil || !found {
			continue
		}
		for _, raw := range []string{reg.AuthorizeURL, reg.TokenURL, reg.RegistrationURL} {
			if h := hostOf(raw); h != "" {
				seen[h] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	return out
}

// --- small helpers ---

// httpClient returns the flow's egress-controlled client, or a
// defaulting one. Discovery/DCR ride the same client as /token so they
// obey the egress allowlist.
func (f *OAuthFlow) httpClient() *http.Client {
	if f.HTTPClient != nil {
		return f.HTTPClient
	}
	return &http.Client{Timeout: defaultDiscoveryTimeout}
}

func fetchJSON[T any](ctx context.Context, client *http.Client, target string) (T, error) {
	var out T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return out, fmt.Errorf("GET %s: status %d", target, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("parsing %s: %w", target, err)
	}
	return out, nil
}

// wellKnown builds a `.well-known/<name>` URL off the ORIGIN of base
// (scheme+host), per RFC 8414 §3 / RFC 9728 §3 — the well-known path
// is rooted at the host, ignoring any path component of base.
func wellKnown(base, name string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = "/.well-known/" + name
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
