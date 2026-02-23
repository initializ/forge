package builtins

import "context"

// webSearchProvider abstracts a web search backend (Tavily, Perplexity, etc.).
type webSearchProvider interface {
	name() string
	search(ctx context.Context, query string, opts webSearchOpts) (string, error)
	egressDomains() []string
}

// webSearchOpts holds optional parameters for a web search request.
type webSearchOpts struct {
	MaxResults     int      `json:"max_results"`
	SearchDepth    string   `json:"search_depth"`
	TimeRange      string   `json:"time_range"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
}
