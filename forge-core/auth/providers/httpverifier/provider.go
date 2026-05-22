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
// Mapping:
//   - HTTP 200 + valid:true   → (Identity, nil)
//   - HTTP 200 + valid:false  → ErrTokenRejected
//   - HTTP 401                → ErrTokenRejected
//   - HTTP other / I/O error  → ErrInvalidToken (wrapped cause)
//
// This provider does not return ErrTokenNotForMe.
func (p *Provider) Verify(ctx context.Context, token string, headers auth.Headers) (*auth.Identity, error) {
	orgID := resolveOrgID(headers, p.cfg.DefaultOrg)

	body, err := json.Marshal(verifyRequest{Token: token, OrgID: orgID})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %w", auth.ErrInvalidToken, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", auth.ErrInvalidToken, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: call verifier: %w", auth.ErrInvalidToken, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to decode body
	case http.StatusUnauthorized:
		return nil, auth.ErrTokenRejected
	default:
		// Drain a bounded amount of body for diagnostics, then fail.
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return nil, fmt.Errorf("%w: verifier returned status %d", auth.ErrInvalidToken, resp.StatusCode)
	}

	var result verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: decode response: %w", auth.ErrInvalidToken, err)
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
