package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/initializ/forge/forge-core/tools"
)

type webSearchTool struct{}

func (t *webSearchTool) Name() string             { return "web_search" }
func (t *webSearchTool) Description() string      { return "Search the web using Tavily or Perplexity AI" }
func (t *webSearchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *webSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"max_results": {"type": "integer", "description": "Maximum number of results (default 5)"},
			"search_depth": {"type": "string", "description": "Search depth: basic or advanced (Tavily only)", "enum": ["basic", "advanced"]},
			"time_range": {"type": "string", "description": "Time range filter: day, week, month, year (Tavily only)"},
			"include_domains": {"type": "array", "items": {"type": "string"}, "description": "Only include results from these domains (Tavily only)"},
			"exclude_domains": {"type": "array", "items": {"type": "string"}, "description": "Exclude results from these domains (Tavily only)"}
		},
		"required": ["query"]
	}`)
}

type webSearchInput struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	SearchDepth    string   `json:"search_depth,omitempty"`
	TimeRange      string   `json:"time_range,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

func (t *webSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input webSearchInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing web_search input: %w", err)
	}
	if input.Query == "" {
		return `{"error": "query is required"}`, nil
	}

	provider, err := resolveWebSearchProvider()
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error()), nil
	}

	opts := webSearchOpts{
		MaxResults:     input.MaxResults,
		SearchDepth:    input.SearchDepth,
		TimeRange:      input.TimeRange,
		IncludeDomains: input.IncludeDomains,
		ExcludeDomains: input.ExcludeDomains,
	}

	return provider.search(ctx, input.Query, opts)
}

// resolveWebSearchProvider selects the web search provider based on environment.
// Priority: WEB_SEARCH_PROVIDER env > auto-detect (Tavily first, then Perplexity).
func resolveWebSearchProvider() (webSearchProvider, error) {
	override := os.Getenv("WEB_SEARCH_PROVIDER")

	switch override {
	case "tavily":
		key := os.Getenv("TAVILY_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("WEB_SEARCH_PROVIDER is set to tavily but TAVILY_API_KEY is not set")
		}
		return newTavilyProvider(key), nil

	case "perplexity":
		key := os.Getenv("PERPLEXITY_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("WEB_SEARCH_PROVIDER is set to perplexity but PERPLEXITY_API_KEY is not set")
		}
		return newPerplexityProvider(key), nil

	case "":
		// Auto-detect: try Tavily first, then Perplexity
		if key := os.Getenv("TAVILY_API_KEY"); key != "" {
			return newTavilyProvider(key), nil
		}
		if key := os.Getenv("PERPLEXITY_API_KEY"); key != "" {
			return newPerplexityProvider(key), nil
		}
		return nil, fmt.Errorf("no web search API key set. Set TAVILY_API_KEY or PERPLEXITY_API_KEY in your .env file to enable web search")

	default:
		return nil, fmt.Errorf("unknown WEB_SEARCH_PROVIDER %q: must be tavily or perplexity", override)
	}
}
