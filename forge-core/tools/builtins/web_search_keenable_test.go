package builtins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestKeenableProvider_Keyless(t *testing.T) {
	var gotPath, gotTitle, gotAPIKey, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("X-Keenable-Title")
		gotAPIKey = r.Header.Get("X-API-Key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": "forge test",
			"results": []map[string]any{
				{"title": "Result 1", "url": "https://example.com", "snippet": "Example snippet"},
			},
		})
	}))
	defer ts.Close()

	p := &keenableProvider{baseURL: ts.URL}
	out, err := p.search(context.Background(), "forge test", webSearchOpts{MaxResults: 3})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if gotPath != "/v1/search/public" {
		t.Errorf("path = %q, want /v1/search/public", gotPath)
	}
	if gotTitle != "Forge" {
		t.Errorf("X-Keenable-Title = %q, want Forge", gotTitle)
	}
	if gotAPIKey != "" {
		t.Errorf("X-API-Key should be empty on the public endpoint, got %q", gotAPIKey)
	}
	if !strings.Contains(gotBody, `"max_results":3`) || !strings.Contains(gotBody, `"query":"forge test"`) {
		t.Errorf("unexpected body: %s", gotBody)
	}
	if !strings.Contains(out, "https://example.com") || !strings.Contains(out, "Example snippet") {
		t.Errorf("unexpected result: %s", out)
	}
	if p.name() != "keenable" {
		t.Errorf("name = %q", p.name())
	}
	if domains := p.egressDomains(); len(domains) != 1 || domains[0] != "api.keenable.ai" {
		t.Errorf("egressDomains = %v", domains)
	}
}

func TestKeenableProvider_Keyed(t *testing.T) {
	var gotPath, gotAPIKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"query": "q", "results": []any{}})
	}))
	defer ts.Close()

	p := &keenableProvider{apiKey: "keen_test", baseURL: ts.URL}
	if _, err := p.search(context.Background(), "q", webSearchOpts{}); err != nil {
		t.Fatalf("search error: %v", err)
	}
	if gotPath != "/v1/search" {
		t.Errorf("path = %q, want /v1/search", gotPath)
	}
	if gotAPIKey != "keen_test" {
		t.Errorf("X-API-Key = %q, want keen_test", gotAPIKey)
	}
}

func TestKeenableProvider_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer ts.Close()

	p := &keenableProvider{baseURL: ts.URL}
	out, err := p.search(context.Background(), "q", webSearchOpts{})
	if err != nil {
		t.Fatalf("search should return the API error in the payload, got err: %v", err)
	}
	if !strings.Contains(out, "status 502") {
		t.Errorf("expected status 502 in payload, got %s", out)
	}
}

func TestResolveWebSearchProvider_Keenable(t *testing.T) {
	origTavily := os.Getenv("TAVILY_API_KEY")
	origPerp := os.Getenv("PERPLEXITY_API_KEY")
	origProvider := os.Getenv("WEB_SEARCH_PROVIDER")
	origKeenable := os.Getenv("KEENABLE_API_KEY")
	_ = os.Unsetenv("TAVILY_API_KEY")
	_ = os.Unsetenv("PERPLEXITY_API_KEY")
	_ = os.Unsetenv("KEENABLE_API_KEY")
	_ = os.Setenv("WEB_SEARCH_PROVIDER", "keenable")
	defer func() {
		restore := func(key, val string) {
			if val != "" {
				_ = os.Setenv(key, val)
			} else {
				_ = os.Unsetenv(key)
			}
		}
		restore("TAVILY_API_KEY", origTavily)
		restore("PERPLEXITY_API_KEY", origPerp)
		restore("WEB_SEARCH_PROVIDER", origProvider)
		restore("KEENABLE_API_KEY", origKeenable)
	}()

	p, err := resolveWebSearchProvider()
	if err != nil {
		t.Fatalf("resolveWebSearchProvider: %v", err)
	}
	if p.name() != "keenable" {
		t.Errorf("name = %q, want keenable", p.name())
	}

	// Auto-detect must not pick Keenable just because it can run without a key.
	_ = os.Unsetenv("WEB_SEARCH_PROVIDER")
	_, err = resolveWebSearchProvider()
	if err == nil || !strings.Contains(err.Error(), "TAVILY_API_KEY") {
		t.Errorf("auto-detect should still require Tavily or Perplexity, got %v", err)
	}
}
