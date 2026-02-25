package oauth

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// ProviderConfig holds the OAuth configuration for a provider.
type ProviderConfig struct {
	AuthURL     string
	TokenURL    string
	ClientID    string
	Scopes      string
	RedirectURI string
	BaseURL     string            // API base URL to use with the obtained token
	ExtraParams map[string]string // additional query params for the auth URL
}

// OpenAIConfig returns the OAuth configuration for OpenAI.
// Uses the same public client ID and endpoints as the official Codex CLI.
// ChatGPT OAuth tokens are scoped to the ChatGPT backend API, not the
// standard OpenAI API (api.openai.com). The base URL is set accordingly.
func OpenAIConfig() ProviderConfig {
	return ProviderConfig{
		AuthURL:     "https://auth.openai.com/oauth/authorize",
		TokenURL:    "https://auth.openai.com/oauth/token",
		ClientID:    "app_EMoamEEZ73f0CkXaXp7hrann",
		Scopes:      "openid profile email offline_access",
		RedirectURI: "http://localhost:1455/auth/callback",
		BaseURL:     "https://chatgpt.com/backend-api/codex",
		ExtraParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
		},
	}
}

// Flow orchestrates the OAuth authorization code flow with PKCE.
type Flow struct {
	Config  ProviderConfig
	Timeout time.Duration // default: 2 minutes
}

// NewFlow creates a new OAuth flow with the given provider config.
func NewFlow(config ProviderConfig) *Flow {
	return &Flow{
		Config:  config,
		Timeout: 2 * time.Minute,
	}
}

// Execute runs the full OAuth flow:
// 1. Generate PKCE params and state
// 2. Start local callback server
// 3. Open browser to authorization URL
// 4. Wait for authorization code
// 5. Exchange code for tokens
// 6. Store credentials
func (f *Flow) Execute(ctx context.Context, provider string) (*Token, error) {
	// Generate PKCE
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	state, err := GenerateState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	// Start callback server on port 1455 (matching redirect_uri)
	callbackServer := NewCallbackServer(1455)
	if err := callbackServer.Start(); err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}
	defer callbackServer.Stop()

	// Build authorization URL
	authURL := f.buildAuthURL(pkce, state)

	// Open browser
	if err := openBrowser(authURL); err != nil {
		return nil, fmt.Errorf("opening browser: %w\n\nPlease open this URL manually:\n%s", err, authURL)
	}

	// Wait for code
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := callbackServer.WaitForCode(waitCtx)
	if err != nil {
		return nil, err
	}

	// Verify state
	if result.State != state {
		return nil, fmt.Errorf("state mismatch: possible CSRF attack")
	}

	// Exchange code for tokens
	token, err := ExchangeCode(
		f.Config.TokenURL,
		f.Config.ClientID,
		result.Code,
		f.Config.RedirectURI,
		pkce.Verifier,
	)
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}

	// Persist the API base URL from config so the correct endpoint is used at runtime
	token.BaseURL = f.Config.BaseURL

	// Store credentials
	if err := SaveCredentials(provider, token); err != nil {
		return nil, fmt.Errorf("saving credentials: %w", err)
	}

	return token, nil
}

func (f *Flow) buildAuthURL(pkce *PKCEParams, state string) string {
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.Config.ClientID},
		"redirect_uri":          {f.Config.RedirectURI},
		"scope":                 {f.Config.Scopes},
		"state":                 {state},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {pkce.Method},
	}
	for k, v := range f.Config.ExtraParams {
		params.Set(k, v)
	}
	return f.Config.AuthURL + "?" + params.Encode()
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
