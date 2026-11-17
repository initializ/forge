package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/initializ/forge/forge-core/security"
)

// keenableProvider implements webSearchProvider using the Keenable Search API.
// A key is optional: with no key the public endpoint is used.
type keenableProvider struct {
	apiKey  string
	baseURL string // defaults to "https://api.keenable.ai"
}

func newKeenableProvider(apiKey string) *keenableProvider {
	return &keenableProvider{apiKey: apiKey, baseURL: "https://api.keenable.ai"}
}

func (p *keenableProvider) name() string { return "keenable" }

func (p *keenableProvider) egressDomains() []string {
	return []string{"api.keenable.ai"}
}

func (p *keenableProvider) search(ctx context.Context, query string, opts webSearchOpts) (string, error) {
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	reqBody := map[string]any{
		"query":       query,
		"max_results": maxResults,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshalling Keenable request: %w", err)
	}

	endpoint := "/v1/search/public"
	if p.apiKey != "" {
		endpoint = "/v1/search"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("creating Keenable request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Keenable-Title", "Forge")
	if p.apiKey != "" {
		httpReq.Header.Set("X-API-Key", p.apiKey)
	}

	client := security.EgressClientFromContext(ctx)
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling Keenable API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Keenable response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf(`{"error": "Keenable API returned status %d: %s"}`, resp.StatusCode, string(respBody)), nil
	}

	var kResp struct {
		Query   string `json:"query"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBody, &kResp); err != nil {
		return "", fmt.Errorf("parsing Keenable response: %w", err)
	}

	result := map[string]any{
		"query": kResp.Query,
	}
	if kResp.Query == "" {
		result["query"] = query
	}
	if len(kResp.Results) > 0 {
		var results []map[string]any
		for _, r := range kResp.Results {
			results = append(results, map[string]any{
				"title":   r.Title,
				"url":     r.URL,
				"content": r.Snippet,
			})
		}
		result["results"] = results
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
