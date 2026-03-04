package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestOpenAIEmbedder_OrgIDHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Organization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "sk-test",
		OrgID:   "org-embed-456",
		BaseURL: srv.URL,
		Model:   "text-embedding-3-small",
		Dims:    3,
	})

	_, err := embedder.Embed(context.Background(), &llm.EmbeddingRequest{
		Texts: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "org-embed-456" {
		t.Errorf("expected OpenAI-Organization header org-embed-456, got %q", gotHeader)
	}
}

func TestOpenAIEmbedder_NoOrgIDHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Organization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:  "sk-test",
		BaseURL: srv.URL,
		Model:   "text-embedding-3-small",
		Dims:    3,
	})

	_, err := embedder.Embed(context.Background(), &llm.EmbeddingRequest{
		Texts: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "" {
		t.Errorf("expected no OpenAI-Organization header, got %q", gotHeader)
	}
}
