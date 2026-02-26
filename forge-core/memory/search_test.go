package memory

import (
	"context"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// mockEmbedder produces deterministic embeddings for testing.
type mockEmbedder struct {
	dims int
}

func (m *mockEmbedder) Dimensions() int { return m.dims }
func (m *mockEmbedder) Embed(_ context.Context, req *llm.EmbeddingRequest) (*llm.EmbeddingResponse, error) {
	embeddings := make([][]float32, len(req.Texts))
	for i, text := range req.Texts {
		// Simple hash-based embedding for deterministic tests.
		vec := make([]float32, m.dims)
		for j, c := range text {
			vec[j%m.dims] += float32(c) / 1000.0
		}
		_ = i
		embeddings[i] = vec
	}
	return &llm.EmbeddingResponse{Embeddings: embeddings, Model: "mock"}, nil
}

func TestHybridSearcher_VectorAndKeyword(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck

	ctx := context.Background()
	emb := &mockEmbedder{dims: 4}

	// Index some chunks.
	now := time.Now().UTC()
	chunks := []IndexedChunk{
		{
			Chunk:  Chunk{ID: "1", Source: "MEMORY.md", Content: "user prefers dark mode theme", CreatedAt: now},
			Vector: []float32{0.5, 0.3, 0.1, 0.0},
		},
		{
			Chunk:  Chunk{ID: "2", Source: "2026-02-20.md", Content: "discussed API rate limiting", CreatedAt: now.Add(-5 * 24 * time.Hour)},
			Vector: []float32{0.1, 0.5, 0.3, 0.0},
		},
		{
			Chunk:  Chunk{ID: "3", Source: "2026-02-24.md", Content: "user wants dark mode for the dashboard", CreatedAt: now.Add(-1 * 24 * time.Hour)},
			Vector: []float32{0.4, 0.3, 0.2, 0.0},
		},
	}
	if err := store.Index(ctx, chunks); err != nil {
		t.Fatal(err)
	}

	searcher := NewHybridSearcher(store, emb, SearchConfig{
		VectorWeight:  0.7,
		KeywordWeight: 0.3,
		DecayHalfLife: 7 * 24 * time.Hour,
		DecayEnabled:  true,
		TopK:          10,
	})

	results, err := searcher.Search(ctx, "dark mode")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	// Both chunks mentioning "dark mode" should be near the top.
	foundDarkMode := false
	for _, r := range results {
		if r.Chunk.ID == "1" || r.Chunk.ID == "3" {
			foundDarkMode = true
			break
		}
	}
	if !foundDarkMode {
		t.Error("expected dark mode chunks in results")
	}
}

func TestHybridSearcher_KeywordOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC()

	chunks := []IndexedChunk{
		{Chunk: Chunk{ID: "1", Source: "MEMORY.md", Content: "user prefers dark mode", CreatedAt: now}},
		{Chunk: Chunk{ID: "2", Source: "daily.md", Content: "unrelated content here", CreatedAt: now}},
	}
	if err := store.Index(ctx, chunks); err != nil {
		t.Fatal(err)
	}

	// No embedder — keyword-only mode.
	searcher := NewHybridSearcher(store, nil, DefaultSearchConfig())

	results, err := searcher.Search(ctx, "dark mode")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Chunk.ID != "1" {
		t.Errorf("expected chunk 1, got %s", results[0].Chunk.ID)
	}
}

func TestHybridSearcher_TemporalDecay(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC()

	// Two chunks with same content but different ages.
	chunks := []IndexedChunk{
		{Chunk: Chunk{ID: "old", Source: "2026-01-01.md", Content: "important fact about config", CreatedAt: now.Add(-30 * 24 * time.Hour)}},
		{Chunk: Chunk{ID: "new", Source: "2026-02-24.md", Content: "important fact about config", CreatedAt: now.Add(-1 * 24 * time.Hour)}},
		{Chunk: Chunk{ID: "evergreen", Source: "MEMORY.md", Content: "important fact about config", CreatedAt: now.Add(-30 * 24 * time.Hour)}},
	}
	if err := store.Index(ctx, chunks); err != nil {
		t.Fatal(err)
	}

	searcher := NewHybridSearcher(store, nil, SearchConfig{
		VectorWeight:  0.7,
		KeywordWeight: 0.3,
		DecayHalfLife: 7 * 24 * time.Hour,
		DecayEnabled:  true,
		TopK:          10,
	})

	results, err := searcher.Search(ctx, "important config")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) < 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// MEMORY.md (evergreen, no decay) and recent chunk should rank higher than old chunk.
	if results[len(results)-1].Chunk.ID != "old" {
		t.Errorf("expected oldest chunk to rank last, got %s", results[len(results)-1].Chunk.ID)
	}
}

func TestTokenize(t *testing.T) {
	tokens := tokenize("Hello, World! Hello")
	if len(tokens) != 2 {
		t.Errorf("expected 2 unique tokens, got %d: %v", len(tokens), tokens)
	}
}

func TestKeywordOverlap(t *testing.T) {
	terms := []string{"hello", "world"}

	overlap := keywordOverlap(terms, "Hello World!")
	if overlap != 1.0 {
		t.Errorf("expected 1.0 overlap, got %f", overlap)
	}

	overlap = keywordOverlap(terms, "Hello there")
	if overlap != 0.5 {
		t.Errorf("expected 0.5 overlap, got %f", overlap)
	}

	overlap = keywordOverlap(terms, "nothing here")
	if overlap != 0.0 {
		t.Errorf("expected 0.0 overlap, got %f", overlap)
	}

	overlap = keywordOverlap(nil, "anything")
	if overlap != 0.0 {
		t.Errorf("expected 0.0 for nil terms, got %f", overlap)
	}
}
