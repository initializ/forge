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
	endpoint := expandEnvVars(p.endpoint)
	identity := expandEnvVars(p.identity)
	if endpoint == "" {
		return "", 0, fmt.Errorf("%w: platform.token_endpoint is empty (or its env var is unset)", ErrProtocolError)
	}
	if identity == "" {
		return "", 0, fmt.Errorf("%w: platform.agent_identity is empty (or its env var is unset) — the platform did not materialize the agent credential", ErrProtocolError)
	}

	body, err := json.Marshal(map[string]string{"server": p.ref})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
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

	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%w: platform token request failed: %v", ErrTransportUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("%w: platform token endpoint returned %d for server %q: %s",
			ErrProtocolError, resp.StatusCode, p.ref, strings.TrimSpace(string(data)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		// A refresh_token here would violate invariant 8 — the platform
		// must never send one; if it does, it is deliberately ignored.
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, fmt.Errorf("%w: parsing platform token response: %v", ErrProtocolError, err)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("%w: platform token response carried no access_token", ErrProtocolError)
	}
	ttl := defaultPlatformTokenTTL
	if out.ExpiresIn > 0 {
		ttl = time.Duration(out.ExpiresIn) * time.Second
	}
	return out.AccessToken, ttl, nil
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
