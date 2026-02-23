package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// perplexityProvider implements webSearchProvider using the Perplexity API.
type perplexityProvider struct {
	apiKey  string
	baseURL string // defaults to "https://api.perplexity.ai"
}

func newPerplexityProvider(apiKey string) *perplexityProvider {
	return &perplexityProvider{apiKey: apiKey, baseURL: "https://api.perplexity.ai"}
}

func (p *perplexityProvider) name() string { return "perplexity" }

func (p *perplexityProvider) egressDomains() []string {
	return []string{"api.perplexity.ai"}
}

func (p *perplexityProvider) search(ctx context.Context, query string, opts webSearchOpts) (string, error) {
	// Perplexity uses the chat completions API with the sonar model.
	// Tavily-specific opts (search_depth, time_range, domains) are ignored gracefully.
	reqBody := map[string]any{
		"model": "sonar",
		"messages": []map[string]string{
			{"role": "user", "content": query},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshalling Perplexity request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("creating Perplexity request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling Perplexity API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Perplexity response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf(`{"error": "Perplexity API returned status %d: %s"}`, resp.StatusCode, string(respBody)), nil
	}

	var pResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Citations []string `json:"citations,omitempty"`
	}
	if err := json.Unmarshal(respBody, &pResp); err != nil {
		return "", fmt.Errorf("parsing Perplexity response: %w", err)
	}

	if len(pResp.Choices) == 0 {
		return `{"error": "no results from Perplexity"}`, nil
	}

	result := map[string]any{
		"query":  query,
		"answer": pResp.Choices[0].Message.Content,
	}
	if len(pResp.Citations) > 0 {
		result["citations"] = pResp.Citations
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
