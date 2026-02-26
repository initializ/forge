package memory

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestFileVectorStore_IndexAndSearch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatalf("NewFileVectorStore: %v", err)
	}

	ctx := context.Background()

	// Index some chunks with vectors.
	chunks := []IndexedChunk{
		{
			Chunk:  Chunk{ID: "1", Source: "test.md", Content: "hello world", CreatedAt: time.Now()},
			Vector: []float32{1, 0, 0},
		},
		{
			Chunk:  Chunk{ID: "2", Source: "test.md", Content: "goodbye world", CreatedAt: time.Now()},
			Vector: []float32{0, 1, 0},
		},
		{
			Chunk:  Chunk{ID: "3", Source: "other.md", Content: "other content", CreatedAt: time.Now()},
			Vector: []float32{0, 0, 1},
		},
	}

	if err := store.Index(ctx, chunks); err != nil {
		t.Fatalf("Index: %v", err)
	}

	if store.Count() != 3 {
		t.Errorf("Count = %d, want 3", store.Count())
	}

	// Search with a vector close to chunk 1.
	results, err := store.Search(ctx, []float32{0.9, 0.1, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Chunk.ID != "1" {
		t.Errorf("top result should be chunk 1, got %s", results[0].Chunk.ID)
	}
}

func TestFileVectorStore_DeleteBySource(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatalf("NewFileVectorStore: %v", err)
	}

	ctx := context.Background()

	chunks := []IndexedChunk{
		{Chunk: Chunk{ID: "1", Source: "a.md"}, Vector: []float32{1, 0}},
		{Chunk: Chunk{ID: "2", Source: "a.md"}, Vector: []float32{0, 1}},
		{Chunk: Chunk{ID: "3", Source: "b.md"}, Vector: []float32{1, 1}},
	}
	if err := store.Index(ctx, chunks); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteBySource(ctx, "a.md"); err != nil {
		t.Fatal(err)
	}

	if store.Count() != 1 {
		t.Errorf("Count after delete = %d, want 1", store.Count())
	}
}

func TestFileVectorStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Create store and index.
	store1, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	chunks := []IndexedChunk{
		{Chunk: Chunk{ID: "1", Source: "test.md", Content: "persist me"}, Vector: []float32{1, 0}},
	}
	if err := store1.Index(ctx, chunks); err != nil {
		t.Fatal(err)
	}
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and verify data persisted.
	store2, err := NewFileVectorStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close() //nolint:errcheck

	if store2.Count() != 1 {
		t.Errorf("Count after reopen = %d, want 1", store2.Count())
	}

	results, err := store2.Search(ctx, []float32{1, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Chunk.Content != "persist me" {
		t.Errorf("expected persisted chunk, got %v", results)
	}
}

func TestCosineSimilarity(t *testing.T) {
	// Same vector → similarity = 1.0
	sim := cosineSimilarity([]float32{1, 0, 0}, []float32{1, 0, 0})
	if math.Abs(sim-1.0) > 1e-6 {
		t.Errorf("same vector similarity = %f, want 1.0", sim)
	}

	// Orthogonal vectors → similarity = 0.0
	sim = cosineSimilarity([]float32{1, 0, 0}, []float32{0, 1, 0})
	if math.Abs(sim) > 1e-6 {
		t.Errorf("orthogonal similarity = %f, want 0.0", sim)
	}

	// Opposite vectors → similarity = -1.0
	sim = cosineSimilarity([]float32{1, 0}, []float32{-1, 0})
	if math.Abs(sim-(-1.0)) > 1e-6 {
		t.Errorf("opposite similarity = %f, want -1.0", sim)
	}

	// Different lengths → 0.0
	sim = cosineSimilarity([]float32{1}, []float32{1, 0})
	if sim != 0 {
		t.Errorf("mismatched lengths = %f, want 0.0", sim)
	}

	// Empty → 0.0
	sim = cosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("empty = %f, want 0.0", sim)
	}
}
