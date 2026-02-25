package oauth

import (
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
func ExchangeCode(tokenURL, clientID, code, redirectURI, codeVerifier string) (*Token, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {codeVerifier},
	}

	return doTokenRequest(tokenURL, data)
}

// RefreshToken exchanges a refresh token for new access and refresh tokens.
func RefreshToken(tokenURL, clientID, refreshToken string) (*Token, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	return doTokenRequest(tokenURL, data)
}

func doTokenRequest(tokenURL string, data url.Values) (*Token, error) {
	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode())) //nolint:gosec
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
