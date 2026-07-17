package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Platform token resolver — the managed half of the resolver seam (design
// §18/§19). An MCP server with auth.type "platform" runs under the
// AGENT-PRINCIPAL (service) identity: Forge POSTs the platform token
// endpoint with the agent's platform credential and receives a SHORT-LIVED
// access token for that server. The resource refresh token never reaches
// the agent (invariant 8) — the platform holds it and refreshes on our
// behalf. Startup-viable: no human, no browser, no stored token.
//
// The token is cached to its TTL and re-fetched on expiry; concurrent
// refreshes collapse behind one flight. Endpoint + identity support ${VAR}
// env expansion resolved at request time, so a rotated pod secret takes
// effect without a restart (same pattern as auth.token_env).

// platformTokenSkew re-fetches slightly before nominal expiry so a token
// is never presented in its final moments.
const platformTokenSkew = 30 * time.Second

// defaultPlatformTokenTTL applies when the endpoint omits expires_in.
const defaultPlatformTokenTTL = 5 * time.Minute

type platformTokenSource struct {
	endpoint string // raw, ${VAR}-expandable
	identity string // raw, ${VAR}-expandable
	ref      string // registry entry ref the platform authorizes against
	client   *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newPlatformTokenSource(cfg PlatformSourceConfig) *platformTokenSource {
	return &platformTokenSource{
		endpoint: cfg.TokenEndpoint,
		identity: cfg.AgentIdentity,
		ref:      cfg.Ref,
		client:   cfg.HTTPClient,
	}
}

// PlatformSourceConfig carries what a platform-auth server needs.
type PlatformSourceConfig struct {
	TokenEndpoint string
	AgentIdentity string
	Ref           string
	HTTPClient    *http.Client
}

// Token returns a valid access token, fetching from the platform when the
// cache is empty or expiring.
func (p *platformTokenSource) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.expiresAt.Add(-platformTokenSkew)) {
		return p.token, nil
	}
	tok, ttl, err := p.fetch(ctx)
	if err != nil {
		return "", err
	}
	p.token = tok
	p.expiresAt = time.Now().Add(ttl)
	return tok, nil
}

func (p *platformTokenSource) fetch(ctx context.Context) (string, time.Duration, error) {
	// Agent-principal: no subject in the body (§19 contract).
	tok, ttl, status, err := doPlatformTokenRequest(ctx, p.client, p.endpoint, p.identity, p.ref, "")
	if err != nil {
		return "", 0, err
	}
	if status != http.StatusOK {
		return "", 0, fmt.Errorf("%w: platform token endpoint returned %d for server %q", ErrProtocolError, status, p.ref)
	}
	return tok, ttl, nil
}

// doPlatformTokenRequest POSTs the platform token endpoint (§19 contract)
// and returns the access token + TTL on 200, or the raw status on non-200
// so the caller can classify (agent-principal vs delegated read a non-200
// differently). Only transport / parse problems return a non-nil err. The
// endpoint + identity are ${VAR}-expanded at request time so a rotated pod
// secret applies without a restart.
//
// Body: {"server": ref} for agent-principal; {"server": ref, "subject": s}
// for the delegated (per-user) path. access_token only — any refresh_token
// is ignored (invariant 8).
func doPlatformTokenRequest(ctx context.Context, client *http.Client, rawEndpoint, rawIdentity, ref, subject string) (token string, ttl time.Duration, status int, err error) {
	endpoint := expandEnvVars(rawEndpoint)
	identity := expandEnvVars(rawIdentity)
	if endpoint == "" {
		return "", 0, 0, fmt.Errorf("%w: platform.token_endpoint is empty (or its env var is unset)", ErrProtocolError)
	}
	if identity == "" {
		return "", 0, 0, fmt.Errorf("%w: platform.agent_identity is empty (or its env var is unset) — the platform did not materialize the agent credential", ErrProtocolError)
	}

	payload := map[string]string{"server": ref}
	if subject != "" {
		payload["subject"] = subject
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+identity)
	// Tenancy headers, exactly as the admission + remote-session-store
	// clients send them: the platform verifies a PER-ORG HS256 token, so it
	// needs Org-Id to select the signing secret BEFORE it can validate the
	// bearer (without this the endpoint 401s "missing org-id header"), and
	// Workspace-Id to authorize the request against the entry (entitlement).
	// Read from the standard FORGE_ORG_ID / FORGE_WORKSPACE_ID env the
	// platform always injects; omitted when empty (standalone/dev).
	if org := os.Getenv("FORGE_ORG_ID"); org != "" {
		req.Header.Set("Org-Id", org)
	}
	if ws := os.Getenv("FORGE_WORKSPACE_ID"); ws != "" {
		req.Header.Set("Workspace-Id", ws)
	}

	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: platform token request failed: %v", ErrTransportUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", 0, resp.StatusCode, nil // caller classifies the non-200
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, resp.StatusCode, fmt.Errorf("%w: parsing platform token response: %v", ErrProtocolError, err)
	}
	if out.AccessToken == "" {
		return "", 0, resp.StatusCode, fmt.Errorf("%w: platform token response carried no access_token", ErrProtocolError)
	}
	ttl = defaultPlatformTokenTTL
	if out.ExpiresIn > 0 {
		ttl = time.Duration(out.ExpiresIn) * time.Second
	}
	return out.AccessToken, ttl, resp.StatusCode, nil
}

// delegatedTokenSource is the managed resolver for auth.type=user (#317):
// a per-REQUESTING-USER access token from the platform token endpoint
// (delegated body {server, subject}), cached per subject so distinct users
// never share a token. A platform 401/403/404 means "no grant for this
// user yet" → ErrNoToken (auth-required), keeping the server lazy and
// non-blocking until the platform-side consent flow produces the grant.
// The refresh token never reaches the agent (invariant 8).
type delegatedTokenSource struct {
	endpoint string
	identity string
	ref      string
	client   *http.Client

	mu    sync.Mutex
	cache map[string]cachedToken // key: subject
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

func newDelegatedTokenSource(cfg PlatformSourceConfig) *delegatedTokenSource {
	return &delegatedTokenSource{
		endpoint: cfg.TokenEndpoint,
		identity: cfg.AgentIdentity,
		ref:      cfg.Ref,
		client:   cfg.HTTPClient,
	}
}

// TokenForSubject returns a valid access token for the requesting user,
// fetching from the platform when the per-subject cache is empty/expiring.
func (d *delegatedTokenSource) TokenForSubject(ctx context.Context, subject string) (string, error) {
	if subject == "" {
		return "", fmt.Errorf("%w: delegated token requires a requesting-user subject", ErrNoToken)
	}
	// Fast path: a cached, unexpired token for this subject. The lock is
	// NOT held across the network fetch, so a slow fetch for user A never
	// blocks user B — the multi-user path is the whole point of #317.
	d.mu.Lock()
	if c, ok := d.cache[subject]; ok && c.token != "" && time.Now().Before(c.expiresAt.Add(-platformTokenSkew)) {
		d.mu.Unlock()
		return c.token, nil
	}
	d.mu.Unlock()

	tok, ttl, status, err := doPlatformTokenRequest(ctx, d.client, d.endpoint, d.identity, d.ref, subject)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
			return "", fmt.Errorf("%w: no platform grant for subject %q on server %q yet — awaiting the delegated consent flow (#317)", ErrNoToken, subject, d.ref)
		}
		return "", fmt.Errorf("%w: platform token endpoint returned %d for server %q (subject %q)", ErrProtocolError, status, d.ref, subject)
	}

	d.mu.Lock()
	if d.cache == nil {
		d.cache = map[string]cachedToken{}
	}
	d.cache[subject] = cachedToken{token: tok, expiresAt: time.Now().Add(ttl)}
	d.mu.Unlock()
	return tok, nil
}

// expandEnvVars resolves ${VAR}/$VAR against the process env at USE time —
// deliberately not at config load, so rotated pod secrets apply without a
// restart. Mirrors the runner's egress-domain expansion semantics.
func expandEnvVars(s string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	return os.Expand(s, os.Getenv)
}
