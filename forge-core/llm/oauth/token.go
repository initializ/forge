package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Token holds the OAuth token data.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
	BaseURL      string    `json:"base_url,omitempty"` // API base URL for this token
}

// IsExpired returns true if the token has expired or will expire within the
// given buffer duration (default 5 minutes).
func (t *Token) IsExpired() bool {
	return t.IsExpiredWithBuffer(5 * time.Minute)
}

// IsExpiredWithBuffer returns true if the token expires within the buffer.
func (t *Token) IsExpiredWithBuffer(buffer time.Duration) bool {
	if t.ExpiresAt.IsZero() {
		return true
	}
	return time.Now().Add(buffer).After(t.ExpiresAt)
}

// tokenResponse is the raw response from the OAuth token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// ExchangeCode exchanges an authorization code for tokens.
//
// Deprecated for new callers — use ExchangeCodeCtx so the request
// is bounded by a context and rides a caller-provided *http.Client.
// Kept for backward compatibility with code written against v0.10.
func ExchangeCode(tokenURL, clientID, code, redirectURI, codeVerifier string) (*Token, error) {
	return ExchangeCodeCtx(context.Background(), nil, tokenURL, clientID, code, redirectURI, codeVerifier)
}

// ExchangeCodeCtx is the context- and client-aware variant of
// ExchangeCode. Caller MUST pass a context with a finite deadline
// (or a cancellable parent) so a hung IdP cannot wedge the goroutine
// indefinitely (review B2).
//
// If client is nil, a sensible defaulting client is constructed with
// a 30s end-to-end timeout — but callers in production should pass
// the egress-controlled client built by security.Resolve so token
// endpoints ride the same allowlist as every other outbound call.
func ExchangeCodeCtx(ctx context.Context, client *http.Client, tokenURL, clientID, code, redirectURI, codeVerifier string) (*Token, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {codeVerifier},
	}
	return doTokenRequestCtx(ctx, client, tokenURL, data)
}

// RefreshToken exchanges a refresh token for new access and refresh tokens.
//
// Deprecated for new callers — use RefreshTokenCtx. See review B2.
func RefreshToken(tokenURL, clientID, refreshToken string) (*Token, error) {
	return RefreshTokenCtx(context.Background(), nil, tokenURL, clientID, refreshToken)
}

// RefreshTokenCtx is the context- and client-aware variant of
// RefreshToken. Caller MUST pass a context with a finite deadline.
// See ExchangeCodeCtx docstring for the client-injection contract.
func RefreshTokenCtx(ctx context.Context, client *http.Client, tokenURL, clientID, refreshToken string) (*Token, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}
	return doTokenRequestCtx(ctx, client, tokenURL, data)
}

// ClientCredentialsTokenCtx mints a token via the OAuth 2.0
// client_credentials grant (RFC 6749 §4.4) — the 2-legged,
// agent-principal path (#324). No user, no authorization code: the
// client authenticates with its own id + secret (client_secret_post).
// The response typically carries no refresh_token; the caller re-mints
// from the id + secret on expiry.
//
// Caller MUST pass a context with a finite deadline; pass the
// egress-controlled client in production (see ExchangeCodeCtx).
func ClientCredentialsTokenCtx(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret string, scopes []string) (*Token, error) {
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	if len(scopes) > 0 {
		data.Set("scope", strings.Join(scopes, " "))
	}
	return doTokenRequestCtx(ctx, client, tokenURL, data)
}

// defaultTokenTimeout bounds the deprecated http.Post fallback path
// used by the no-ctx legacy callers. New callers go through
// *Ctx helpers with their own bounded context.
const defaultTokenTimeout = 30 * time.Second

func doTokenRequestCtx(ctx context.Context, client *http.Client, tokenURL string, data url.Values) (*Token, error) {
	if client == nil {
		// Defaulting client gets the same 30s cap a sensible operator
		// would set — the previous behavior was http.DefaultClient
		// with NO timeout, which is the bug this fix closes (B2).
		client = &http.Client{Timeout: defaultTokenTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tr.Error != "" {
		return nil, fmt.Errorf("oauth error: %s — %s", tr.Error, tr.ErrorDesc)
	}

	if tr.AccessToken == "" {
		return nil, fmt.Errorf("no access token in response")
	}

	token := &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		ExpiresIn:    tr.ExpiresIn,
		Scope:        tr.Scope,
	}

	if tr.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}

	return token, nil
}
