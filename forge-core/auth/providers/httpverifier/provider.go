// Package httpverifier implements the legacy external auth provider:
// POST a JSON envelope to a verifier URL and trust its response.
//
// This is the provider that the historical --auth-url / FORGE_AUTH_URL flag
// configures. The request/response shape is preserved byte-for-byte so
// existing custom verifier services keep working unchanged.
//
//	Request:  POST {URL}
//	          Content-Type: application/json
//	          { "token": "<bearer>", "org_id": "<org-id>" }
//
//	Response: 200 OK
//	          { "valid": bool, "error": "...", "user_id": "...",
//	            "org_id": "...", "email": "...", "workspace_id": "..." }
//
// The HTTP verifier claims every token presented to it — it never returns
// ErrTokenNotForMe. When placed in a chain, it is typically the terminator.
package httpverifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// ProviderName is the type name used to register and reference this provider.
const ProviderName = "http_verifier"

// DefaultTimeout is the per-request timeout used when Config.Timeout is unset.
const DefaultTimeout = 10 * time.Second

// Config controls the http_verifier provider.
type Config struct {
	// URL is the verifier endpoint. Required.
	URL string `yaml:"url"`

	// DefaultOrg is the org_id sent to the verifier when no X-Org-ID
	// header (or org-id / org_id variant) is present on the request.
	DefaultOrg string `yaml:"default_org,omitempty"`

	// Timeout caps each verify call. Defaults to DefaultTimeout.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// HTTPClient overrides the default client. Injectable for tests.
	HTTPClient *http.Client `yaml:"-"`
}

// Validate returns ErrProviderNotConfigured when required fields are missing.
func (c Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("%w: url required", auth.ErrProviderNotConfigured)
	}
	return nil
}

// Provider implements auth.Provider against a remote HTTP verifier.
type Provider struct {
	cfg    Config
	client *http.Client
}

// New constructs a Provider after validating cfg.
func New(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Provider{cfg: cfg, client: client}, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// verifyRequest is the JSON envelope POSTed to the verifier URL.
// Preserved byte-for-byte from the pre-refactor implementation.
type verifyRequest struct {
	Token string `json:"token"`
	OrgID string `json:"org_id"`
}

// verifyResponse is the JSON envelope returned by the verifier URL.
// Preserved byte-for-byte from the pre-refactor implementation.
type verifyResponse struct {
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	OrgID       string `json:"org_id,omitempty"`
	Email       string `json:"email,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// Verify implements auth.Provider. It POSTs the bearer token (with the
// caller's org_id) to the configured verifier URL and translates the
// response into an Identity or one of the sentinel errors.
//
// Mapping (review #6 — separated "token bad" from "verifier down"):
//
//   - HTTP 200 + valid:true              → (Identity, nil)
//   - HTTP 200 + valid:false             → ErrTokenRejected
//   - HTTP 401                           → ErrTokenRejected
//   - HTTP 4xx (other)                   → ErrTokenRejected (verifier denied — token-side)
//   - HTTP 5xx                           → ErrProviderUnavailable (server-side failure)
//   - Network / transport error          → ErrProviderUnavailable
//   - Response body undecodable          → ErrProviderUnavailable (verifier returned garbage)
//   - Local marshal / request-build err  → ErrInvalidToken (extremely rare; bug in caller path)
//
// This provider does not return ErrTokenNotForMe.
func (p *Provider) Verify(ctx context.Context, token string, headers auth.Headers) (*auth.Identity, error) {
	orgID := resolveOrgID(headers, p.cfg.DefaultOrg)

	body, err := json.Marshal(verifyRequest{Token: token, OrgID: orgID})
	if err != nil {
		// Local marshal failure — should never happen with the fixed
		// struct shape. Keep ErrInvalidToken since this isn't the
		// verifier's fault.
		return nil, fmt.Errorf("%w: marshal request: %w", auth.ErrInvalidToken, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", auth.ErrInvalidToken, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		// Network error reaching the verifier — provider is unreachable.
		// Distinct from a token problem; audit reflects this.
		return nil, fmt.Errorf("%w: call verifier: %w", auth.ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		// fall through to decode body
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, auth.ErrTokenRejected
	case resp.StatusCode >= 500 && resp.StatusCode < 600:
		// 5xx — verifier broken or overloaded. We can't say whether the
		// token is valid; surface as provider unavailable.
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return nil, fmt.Errorf("%w: verifier returned status %d", auth.ErrProviderUnavailable, resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Non-401 4xx — verifier explicitly refused the request (e.g.,
		// 400 bad request, 403 forbidden). Treat as token-side rejection.
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return nil, fmt.Errorf("%w: verifier returned status %d", auth.ErrTokenRejected, resp.StatusCode)
	default:
		// 1xx / 3xx — undefined for this contract. Treat as unavailable.
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return nil, fmt.Errorf("%w: verifier returned unexpected status %d", auth.ErrProviderUnavailable, resp.StatusCode)
	}

	var result verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Verifier returned 200 but the body isn't the contract shape —
		// provider misbehavior, not a token issue.
		return nil, fmt.Errorf("%w: decode response: %w", auth.ErrProviderUnavailable, err)
	}
	if !result.Valid {
		return nil, auth.ErrTokenRejected
	}

	return &auth.Identity{
		UserID:      result.UserID,
		Email:       result.Email,
		OrgID:       result.OrgID,
		WorkspaceID: result.WorkspaceID,
		Source:      ProviderName,
	}, nil
}

// resolveOrgID picks the org_id sent to the verifier with this precedence:
// X-Org-ID header > "org-id" > "org_id" > config default. Matches the
// pre-refactor behavior in middleware.go.
func resolveOrgID(headers auth.Headers, fallback string) string {
	for _, key := range []string{"X-Org-ID", "org-id", "org_id"} {
		if v := headers.Get(key); v != "" {
			return v
		}
	}
	return fallback
}

func init() {
	auth.Register(ProviderName, func(settings map[string]any) (auth.Provider, error) {
		var cfg Config
		if err := auth.UnmarshalSettings(settings, &cfg); err != nil {
			return nil, err
		}
		return New(cfg)
	})
}
