package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// OSCommandExecutor implements tools.CommandExecutor using os/exec.
type OSCommandExecutor struct{}

func (e *OSCommandExecutor) Run(ctx context.Context, command string, args []string, stdin []byte) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("command error: %s", stderr.String())
		}
		return "", fmt.Errorf("command execution failed: %w", err)
	}

	return stdout.String(), nil
}

// SkillCommandExecutor implements tools.CommandExecutor with a configurable
// timeout and environment variable passthrough for skill scripts.
type SkillCommandExecutor struct {
	Timeout  time.Duration
	WorkDir  string   // agent working directory — script paths are relative to this
	EnvVars  []string // extra env var names to pass through (e.g., "TAVILY_API_KEY")
	ProxyURL string   // egress proxy URL (e.g., "http://127.0.0.1:54321")
	Model    string   // configured LLM model name — passed as REVIEW_MODEL to skill scripts
}

func (e *SkillCommandExecutor) Run(ctx context.Context, command string, args []string, stdin []byte) (string, error) {
	timeout := e.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	if e.WorkDir != "" {
		cmd.Dir = e.WorkDir
	}

	// Build minimal environment with only explicitly allowed variables.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	for _, name := range e.EnvVars {
		if val := os.Getenv(name); val != "" {
			// Resolve OAuth sentinel to actual access token so skill
			// scripts can call the LLM API directly via curl.
			if val == "__oauth__" && name == "OPENAI_API_KEY" {
				if resolved := resolveOAuthToken(); resolved != nil {
					val = resolved.AccessToken
					if resolved.BaseURL != "" {
						env = append(env, "OPENAI_BASE_URL="+resolved.BaseURL)
					}
				}
			}
			env = append(env, name+"="+val)
		}
	}
	if orgID := os.Getenv("OPENAI_ORG_ID"); orgID != "" {
		env = append(env, "OPENAI_ORG_ID="+orgID)
	}
	// Pass configured model to skill scripts (e.g., code-review uses REVIEW_MODEL).
	// This ensures OAuth/Codex users get a compatible model instead of the
	// script's default (gpt-4o) which may not be supported.
	if e.Model != "" && os.Getenv("REVIEW_MODEL") == "" {
		env = append(env, "REVIEW_MODEL="+e.Model)
	}
	if e.ProxyURL != "" {
		env = append(env,
			"HTTP_PROXY="+e.ProxyURL,
			"HTTPS_PROXY="+e.ProxyURL,
			"http_proxy="+e.ProxyURL,
			"https_proxy="+e.ProxyURL,
		)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("skill command error: %s", stderr.String())
		}
		return "", fmt.Errorf("skill command execution failed: %w", err)
	}

	return stdout.String(), nil
}

// resolveOAuthToken loads and refreshes the OpenAI OAuth token.
// Returns nil if no valid token is available.
func resolveOAuthToken() *oauth.Token {
	token, err := oauth.LoadCredentials("openai")
	if err != nil || token == nil {
		return nil
	}
	if token.IsExpired() && token.RefreshToken != "" {
		oauthCfg := oauth.OpenAIConfig()
		newToken, rErr := oauth.RefreshToken(oauthCfg.TokenURL, oauthCfg.ClientID, token.RefreshToken)
		if rErr != nil {
			return nil
		}
		if newToken.RefreshToken == "" {
			newToken.RefreshToken = token.RefreshToken
		}
		if newToken.BaseURL == "" {
			newToken.BaseURL = token.BaseURL
		}
		_ = oauth.SaveCredentials("openai", newToken)
		return newToken
	}
	return token
}
