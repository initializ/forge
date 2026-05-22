package msteams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AuthFlow identifies the OAuth2 grant the adapter uses.
type AuthFlow string

const (
	// FlowDelegated uses a long-lived refresh token captured at setup time
	// (via the device-code flow) to obtain access tokens that act as a
	// specific user. Refresh tokens rotate — the new token returned with
	// each refresh response should be persisted back to the secret store.
	FlowDelegated AuthFlow = "delegated"

	// FlowClientCredentials uses a client_id + client_secret pair to obtain
	// app-only tokens. Requires admin consent + appropriate application
	// permissions (RSC) on chats. user_id must be configured explicitly.
	FlowClientCredentials AuthFlow = "client_credentials"
)

// defaultGraphScope is the .default scope marker that requests all
// statically-consented permissions for the configured Entra app.
const defaultGraphScope = "https://graph.microsoft.com/.default offline_access"

// authConfig is the subset of adapter Config the auth manager needs.
type authConfig struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	RefreshToken string
	Flow         AuthFlow

	// LoginBaseURL is overridable for tests and sovereign clouds.
	// Default: https://login.microsoftonline.com
	LoginBaseURL string

	// OnRefreshTokenRotated is invoked whenever the delegated flow returns a
	// new refresh token. Implementations should persist the new value back to
	// the secret store. Best-effort; errors are logged but not fatal.
	OnRefreshTokenRotated func(newRefreshToken string)
}

// tokenResponse mirrors the JSON returned by the v2.0 token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"` // only set for delegated flow
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// authManager acquires and caches Microsoft Graph access tokens. Token
// refresh is lazy: callers ask for a token, and the manager returns the
// cached one if it has at least 60 s of life left, otherwise it refreshes.
type authManager struct {
	cfg    authConfig
	client *http.Client

	mu        sync.Mutex
	cached    string
	expiresAt time.Time

	// publicClientInferred tracks whether Entra previously rejected our
	// token request with AADSTS700025 ("Client is public so neither
	// 'client_assertion' nor 'client_secret' should be presented"). When
	// true, refreshLocked omits client_secret on subsequent calls — saves
	// a round-trip per refresh after the first detection. Entra's "Allow
	// public client flows" toggle determines this; we can't read the app
	// registration from inside the agent, so we infer from server errors.
	publicClientInferred bool
}

func newAuthManager(cfg authConfig, client *http.Client) *authManager {
	if cfg.LoginBaseURL == "" {
		cfg.LoginBaseURL = "https://login.microsoftonline.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &authManager{cfg: cfg, client: client}
}

// Token returns a valid access token, refreshing if necessary. Safe for
// concurrent use; one refresh at a time.
func (a *authManager) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cached != "" && time.Until(a.expiresAt) > 60*time.Second {
		return a.cached, nil
	}
	return a.refreshLocked(ctx)
}

// ForceRefresh discards the cached token and acquires a new one. Used after
// a 401 from Graph to recover from a server-side token revocation.
func (a *authManager) ForceRefresh(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cached = ""
	a.expiresAt = time.Time{}
	return a.refreshLocked(ctx)
}

// refreshLocked performs the token request. Caller must hold a.mu.
//
// On the delegated flow it first tries with client_secret if available and the
// app type isn't already known to be public. If Entra rejects with
// AADSTS700025 ("Client is public so neither 'client_assertion' nor
// 'client_secret' should be presented"), the call automatically retries
// without the secret and caches the inferred app type to skip the secret on
// subsequent refreshes.
func (a *authManager) refreshLocked(ctx context.Context) (string, error) {
	if a.cfg.TenantID == "" {
		return "", errors.New("msteams auth: tenant_id is required")
	}
	if a.cfg.ClientID == "" {
		return "", errors.New("msteams auth: client_id is required")
	}

	tr, used, err := a.requestToken(ctx, true)
	if err == nil {
		// Cache app-type inference based on what actually worked.
		if !used && a.cfg.ClientSecret != "" {
			a.publicClientInferred = true
		}
		a.cached = tr.AccessToken
		a.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		if tr.RefreshToken != "" && tr.RefreshToken != a.cfg.RefreshToken {
			a.cfg.RefreshToken = tr.RefreshToken
			if a.cfg.OnRefreshTokenRotated != nil {
				a.cfg.OnRefreshTokenRotated(tr.RefreshToken)
			}
		}
		return a.cached, nil
	}

	return "", err
}

// requestToken performs a single token-endpoint POST and handles the
// public-vs-confidential auto-detect retry. The bool return reports whether
// client_secret was actually sent on the successful request — the caller uses
// it to update publicClientInferred.
func (a *authManager) requestToken(ctx context.Context, allowRetryWithoutSecret bool) (*tokenResponse, bool, error) {
	useSecret := a.cfg.ClientSecret != "" && !a.publicClientInferred

	tr, err := a.postTokenForm(ctx, useSecret)
	if err == nil {
		return tr, useSecret, nil
	}

	// AADSTS700025 = Entra registered this app as public; secret rejected.
	// Retry once without the secret, then cache the inference.
	if allowRetryWithoutSecret && useSecret && isPublicClientError(err) {
		tr2, err2 := a.postTokenForm(ctx, false)
		if err2 != nil {
			return nil, false, err2
		}
		return tr2, false, nil
	}

	return nil, useSecret, err
}

// postTokenForm builds and submits the form-encoded /token request. When
// withSecret is true and the configured flow accepts a secret, the
// client_secret parameter is included.
func (a *authManager) postTokenForm(ctx context.Context, withSecret bool) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("scope", defaultGraphScope)

	switch a.cfg.Flow {
	case FlowDelegated:
		if a.cfg.RefreshToken == "" {
			return nil, errors.New("msteams auth: refresh_token is required for delegated flow")
		}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", a.cfg.RefreshToken)
		if withSecret && a.cfg.ClientSecret != "" {
			form.Set("client_secret", a.cfg.ClientSecret)
		}
	case FlowClientCredentials:
		// Client credentials is inherently confidential — public-client
		// inference does not apply.
		if a.cfg.ClientSecret == "" {
			return nil, errors.New("msteams auth: client_secret is required for client_credentials flow")
		}
		form.Set("grant_type", "client_credentials")
		form.Set("client_secret", a.cfg.ClientSecret)
		form.Set("scope", "https://graph.microsoft.com/.default")
	default:
		return nil, fmt.Errorf("msteams auth: unknown flow %q (want delegated or client_credentials)", a.cfg.Flow)
	}

	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", a.cfg.LoginBaseURL, a.cfg.TenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("msteams auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("msteams auth: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("msteams auth: read body: %w", err)
	}

	var tr tokenResponse
	if jerr := json.Unmarshal(body, &tr); jerr != nil {
		return nil, fmt.Errorf("msteams auth: decode response (status=%d): %w", resp.StatusCode, jerr)
	}

	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("msteams auth: token endpoint error: %s", msg)
	}

	return &tr, nil
}

// isPublicClientError returns true when Entra signals the configured app is
// a public client and rejects the call for including client credentials.
func isPublicClientError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "AADSTS700025")
}
