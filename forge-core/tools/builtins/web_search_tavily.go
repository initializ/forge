package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// tavilyProvider implements webSearchProvider using the Tavily API.
type tavilyProvider struct {
	apiKey  string
	baseURL string // defaults to "https://api.tavily.com"
}

func newTavilyProvider(apiKey string) *tavilyProvider {
	return &tavilyProvider{apiKey: apiKey, baseURL: "https://api.tavily.com"}
}

func (p *tavilyProvider) name() string { return "tavily" }

func (p *tavilyProvider) egressDomains() []string {
	return []string{"api.tavily.com"}
}

func (p *tavilyProvider) search(ctx context.Context, query string, opts webSearchOpts) (string, error) {
	reqBody := map[string]any{
		"query": query,
	}
	if opts.MaxResults > 0 {
		reqBody["max_results"] = opts.MaxResults
	}
	if opts.SearchDepth != "" {
		reqBody["search_depth"] = opts.SearchDepth
	}
	if opts.TimeRange != "" {
		reqBody["time_range"] = opts.TimeRange
	}
	if len(opts.IncludeDomains) > 0 {
		reqBody["include_domains"] = opts.IncludeDomains
	}
	if len(opts.ExcludeDomains) > 0 {
		reqBody["exclude_domains"] = opts.ExcludeDomains
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshalling Tavily request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("creating Tavily request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling Tavily API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Tavily response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf(`{"error": "Tavily API returned status %d: %s"}`, resp.StatusCode, string(respBody)), nil
	}

	// Parse the Tavily response
	var tResp struct {
		Query        string  `json:"query"`
		ResponseTime float64 `json:"response_time"`
		Answer       string  `json:"answer,omitempty"`
		Results      []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBody, &tResp); err != nil {
		return "", fmt.Errorf("parsing Tavily response: %w", err)
	}

	result := map[string]any{
		"query":         tResp.Query,
		"response_time": tResp.ResponseTime,
	}
	if tResp.Answer != "" {
		result["answer"] = tResp.Answer
	}
	if len(tResp.Results) > 0 {
		var results []map[string]any
		for _, r := range tResp.Results {
			results = append(results, map[string]any{
				"title":   r.Title,
				"url":     r.URL,
				"content": r.Content,
				"score":   r.Score,
			})
		}
		result["results"] = results
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
