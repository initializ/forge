package providers

import (
	"context"
	"fmt"
	"sync"

	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/oauth"
)

// OAuthClient wraps a ResponsesClient with automatic OAuth token refresh.
// It implements llm.Client and transparently refreshes expired tokens
// before each API call. ChatGPT OAuth tokens are scoped to the Responses API,
// not the Chat Completions API, so this client uses the Responses API format.
type OAuthClient struct {
	inner    *ResponsesClient
	provider string
	config   oauth.ProviderConfig
	mu       sync.Mutex
}

// NewOAuthClient creates a new OAuth-aware client that uses the Responses API.
// The token is loaded from stored credentials and refreshed automatically.
func NewOAuthClient(cfg llm.ClientConfig, provider string, oauthConfig oauth.ProviderConfig) *OAuthClient {
	inner := NewResponsesClient(cfg)
	// ChatGPT Codex backend requires store=false
	inner.disableStore = true
	return &OAuthClient{
		inner:    inner,
		provider: provider,
		config:   oauthConfig,
	}
}

// Chat sends a Responses API request, refreshing the token if needed.
func (c *OAuthClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := c.ensureValidToken(); err != nil {
		return nil, err
	}
	return c.inner.Chat(ctx, req)
}

// ChatStream sends a streaming Responses API request, refreshing the token if needed.
func (c *OAuthClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	if err := c.ensureValidToken(); err != nil {
		return nil, err
	}
	return c.inner.ChatStream(ctx, req)
}

// ModelID returns the model identifier.
func (c *OAuthClient) ModelID() string {
	return c.inner.ModelID()
}

// ensureValidToken checks if the stored token is expired and refreshes it.
func (c *OAuthClient) ensureValidToken() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	token, err := oauth.LoadCredentials(c.provider)
	if err != nil {
		return fmt.Errorf("loading OAuth credentials: %w", err)
	}
	if token == nil {
		return fmt.Errorf("no OAuth credentials found for %s", c.provider)
	}

	if !token.IsExpired() {
		return nil
	}

	if token.RefreshToken == "" {
		return fmt.Errorf("OAuth token expired and no refresh token available for %s", c.provider)
	}

	// Refresh the token
	newToken, err := oauth.RefreshToken(c.config.TokenURL, c.config.ClientID, token.RefreshToken)
	if err != nil {
		return fmt.Errorf("refreshing OAuth token: %w", err)
	}

	// Preserve refresh token if not returned in refresh response
	if newToken.RefreshToken == "" {
		newToken.RefreshToken = token.RefreshToken
	}

	// Preserve the base URL from the original token
	if newToken.BaseURL == "" {
		newToken.BaseURL = token.BaseURL
	}

	// Persist the new token
	if err := oauth.SaveCredentials(c.provider, newToken); err != nil {
		return fmt.Errorf("saving refreshed token: %w", err)
	}

	// Update the inner client's API key
	c.inner.apiKey = newToken.AccessToken

	return nil
}
