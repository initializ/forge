package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// providerValidationURLs maps provider names to their validation endpoints.
// Exported as variables to allow overriding in tests.
var (
	openaiValidationURL     = "https://api.openai.com/v1/models"
	anthropicValidationURL  = "https://api.anthropic.com/v1/messages"
	geminiValidationURL     = "https://generativelanguage.googleapis.com/v1beta/models"
	ollamaValidationURL     = "http://localhost:11434/api/tags"
	perplexityValidationURL = "https://api.perplexity.ai/chat/completions"
)

// validateProviderKey validates an API key against the specified provider.
// Returns nil on success, a descriptive error on failure.
func validateProviderKey(provider, apiKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch provider {
	case "openai":
		return validateOpenAIKey(ctx, apiKey)
	case "anthropic":
		return validateAnthropicKey(ctx, apiKey)
	case "gemini":
		return validateGeminiKey(ctx, apiKey)
	case "ollama":
		return validateOllamaConnection(ctx)
	case "custom":
		return nil // no validation for custom providers
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

func validateOpenAIKey(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openaiValidationURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to OpenAI: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid OpenAI API key (401 Unauthorized)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenAI API returned status %d", resp.StatusCode)
	}
	return nil
}

func validateAnthropicKey(ctx context.Context, apiKey string) error {
	// Use a minimal messages request to validate the key.
	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicValidationURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Anthropic: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid Anthropic API key (401 Unauthorized)")
	}
	// A 200 or even 400 (bad request shape) means the key itself is valid
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest {
		return nil
	}
	return fmt.Errorf("anthropic API returned status %d", resp.StatusCode)
}

func validateGeminiKey(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiValidationURL+"?key="+apiKey, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Gemini: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("invalid Gemini API key (%d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gemini API returned status %d", resp.StatusCode)
	}
	return nil
}

func validateOllamaConnection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaValidationURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Ollama at %s: %w", ollamaValidationURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}
	return nil
}

// validatePerplexityKey validates a Perplexity API key with a minimal request.
func validatePerplexityKey(apiKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	body := map[string]any{
		"model":    "sonar",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, perplexityValidationURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Perplexity: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid Perplexity API key (401 Unauthorized)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("perplexity API returned status %d", resp.StatusCode)
	}
	return nil
}
