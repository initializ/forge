package oauth

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
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
	// BrowserOpener opens the authorize URL; defaults to the platform opener.
	// Overridable (e.g. in tests to simulate the IdP redirect to the callback).
	BrowserOpener func(url string) error
}

// NewFlow creates a new OAuth flow with the given provider config.
func NewFlow(config ProviderConfig) *Flow {
	return &Flow{
		Config:        config,
		Timeout:       2 * time.Minute,
		BrowserOpener: openBrowser,
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

	// Derive the callback listen address + path from redirect_uri, so the
	// server binds exactly what the IdP redirects to (a configurable loopback,
	// #490). Must be an http loopback — that is the security boundary for
	// binding a local listener.
	addr, path, err := loopbackCallback(f.Config.RedirectURI)
	if err != nil {
		return nil, err
	}
	callbackServer := NewCallbackServer(addr, path)
	if err := callbackServer.Start(); err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}
	defer callbackServer.Stop()

	// Build authorization URL
	authURL := f.buildAuthURL(pkce, state)

	// Open browser (non-fatal: print the URL for manual paste on failure).
	open := f.BrowserOpener
	if open == nil {
		open = openBrowser
	}
	if err := open(authURL); err != nil {
		fmt.Printf("Open this URL to sign in:\n%s\n", authURL)
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

	// Exchange code for tokens (ctx-aware).
	token, err := ExchangeCodeCtx(
		waitCtx, nil,
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

// loopbackCallback derives the callback listen address ("host:port") and path
// from an OAuth redirect_uri. An empty redirect defaults to the historical
// http://localhost:1455/auth/callback. The redirect MUST be an http loopback
// (localhost / 127.0.0.1 / [::1]) — binding a local listener to anything else
// is the security boundary this enforces (#490 configurable loopback).
func loopbackCallback(redirectURI string) (addr, path string, err error) {
	if redirectURI == "" {
		return "localhost:1455", "/auth/callback", nil
	}
	u, perr := url.Parse(redirectURI)
	if perr != nil || u.Host == "" {
		return "", "", fmt.Errorf("invalid redirect_uri %q", redirectURI)
	}
	if !strings.EqualFold(u.Scheme, "http") || !isLoopback(u.Hostname()) {
		return "", "", fmt.Errorf("redirect_uri must be an http loopback (localhost/127.0.0.1/[::1]), got %q", redirectURI)
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return u.Host, p, nil
}

// isLoopback reports whether host is a loopback host literal.
func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) error {
	cmd := browserCommand(runtime.GOOS, url)
	if cmd == nil {
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// browserCommand builds (but does not start) the platform launcher for
// url, so the selection table is unit-testable — a shell-truncation
// regression is invisible to a URL-shape test. Returns nil for an
// unsupported GOOS. The url is always passed as a single argument,
// never through a shell, so `&` in query strings survives.
//
// Windows note: `cmd /c start <url>` treats `&` as the shell "AND"
// separator, truncating any URL that has more than one query
// parameter — the OpenAI OAuth authorize URL has eight, so the browser
// opens with only `?response_type=code` and OpenAI's auth server
// returns a generic `unknown_error`. `rundll32
// url.dll,FileProtocolHandler` opens URLs through the Windows shell API
// without invoking cmd's parser, so `&` stays intact across Windows
// Terminal / PowerShell / cmd.exe.
func browserCommand(goos, url string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "linux":
		return exec.Command("xdg-open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return nil
	}
}
