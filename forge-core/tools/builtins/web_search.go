package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/initializ/forge/forge-core/tools"
)

type webSearchTool struct{}

func (t *webSearchTool) Name() string            { return "web_search" }
func (t *webSearchTool) Description() string     { return "Search the web using Perplexity AI" }
func (t *webSearchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *webSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"max_results": {"type": "integer", "description": "Maximum number of results (default 5)"}
		},
		"required": ["query"]
	}`)
}

type webSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

func (t *webSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	apiKey := os.Getenv("PERPLEXITY_API_KEY")
	if apiKey == "" {
		return `{"error": "PERPLEXITY_API_KEY is not set. Add it to your .env file to enable web search."}`, nil
	}

	var input webSearchInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing web_search input: %w", err)
	}
	if input.Query == "" {
		return `{"error": "query is required"}`, nil
	}

	// Build Perplexity chat completion request
	reqBody := map[string]any{
		"model": "sonar",
		"messages": []map[string]string{
			{"role": "user", "content": input.Query},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.perplexity.ai/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling Perplexity API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf(`{"error": "Perplexity API returned status %d: %s"}`, resp.StatusCode, string(respBody)), nil
	}

	// Extract the answer from the response
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
		"query":  input.Query,
		"answer": pResp.Choices[0].Message.Content,
	}
	if len(pResp.Citations) > 0 {
		result["citations"] = pResp.Citations
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
