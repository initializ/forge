package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// VectorStore is the pluggable interface for vector storage backends.
// FileVectorStore is the initial implementation; swap to Qdrant/Pinecone later.
type VectorStore interface {
	// Index adds or updates chunks with their embedding vectors.
	Index(ctx context.Context, chunks []IndexedChunk) error
	// Search returns the top-k most similar chunks to the query vector.
	Search(ctx context.Context, queryVector []float32, k int) ([]SearchResult, error)
	// DeleteBySource removes all chunks from a given source file.
	DeleteBySource(ctx context.Context, sourceFile string) error
	// Count returns the total number of indexed chunks.
	Count() int
	// Close flushes any pending writes and releases resources.
	Close() error
}

// IndexedChunk is a Chunk with its embedding vector.
type IndexedChunk struct {
	Chunk  Chunk     `json:"chunk"`
	Vector []float32 `json:"vector"`
}

// SearchResult is a chunk with its similarity score.
type SearchResult struct {
	Chunk Chunk   `json:"chunk"`
	Score float64 `json:"score"`
}

// FileVectorStore is a JSON file-backed VectorStore.
// All data is loaded into memory; suitable for corpora under ~10K chunks.
type FileVectorStore struct {
	mu     sync.RWMutex
	dir    string // index directory (e.g. .forge/memory/index)
	chunks map[string]IndexedChunk
	dirty  bool
}

// NewFileVectorStore opens or creates a file-based vector store in dir.
func NewFileVectorStore(dir string) (*FileVectorStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating index dir: %w", err)
	}

	store := &FileVectorStore{
		dir:    dir,
		chunks: make(map[string]IndexedChunk),
	}

	if err := store.load(); err != nil {
		// Corrupted index — start fresh.
		store.chunks = make(map[string]IndexedChunk)
	}

	return store, nil
}

// Index adds or updates indexed chunks. Thread-safe.
func (s *FileVectorStore) Index(_ context.Context, chunks []IndexedChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range chunks {
		s.chunks[c.Chunk.ID] = c
	}
	s.dirty = true
	return nil
}

// Search performs a linear scan with cosine similarity. Thread-safe.
func (s *FileVectorStore) Search(_ context.Context, queryVector []float32, k int) ([]SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if k <= 0 {
		k = 10
	}

	// Compute similarities and maintain a top-k list.
	results := make([]SearchResult, 0, k)
	for _, ic := range s.chunks {
		score := cosineSimilarity(queryVector, ic.Vector)
		if len(results) < k {
			results = append(results, SearchResult{Chunk: ic.Chunk, Score: score})
			// Bubble up (insertion sort for small k).
			for i := len(results) - 1; i > 0 && results[i].Score > results[i-1].Score; i-- {
				results[i], results[i-1] = results[i-1], results[i]
			}
		} else if score > results[k-1].Score {
			results[k-1] = SearchResult{Chunk: ic.Chunk, Score: score}
			for i := k - 1; i > 0 && results[i].Score > results[i-1].Score; i-- {
				results[i], results[i-1] = results[i-1], results[i]
			}
		}
	}

	return results, nil
}

// DeleteBySource removes all chunks from a given source file.
func (s *FileVectorStore) DeleteBySource(_ context.Context, sourceFile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, ic := range s.chunks {
		if ic.Chunk.Source == sourceFile {
			delete(s.chunks, id)
			s.dirty = true
		}
	}
	return nil
}

// Count returns the number of indexed chunks.
func (s *FileVectorStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

// Close flushes dirty data to disk.
func (s *FileVectorStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}
	return s.flush()
}

// load reads chunks from the JSON index file.
func (s *FileVectorStore) load() error {
	path := filepath.Join(s.dir, "index.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var chunks []IndexedChunk
	if err := json.Unmarshal(data, &chunks); err != nil {
		return fmt.Errorf("decoding index: %w", err)
	}

	for _, c := range chunks {
		s.chunks[c.Chunk.ID] = c
	}
	return nil
}

// flush writes all chunks to the JSON index file atomically.
func (s *FileVectorStore) flush() error {
	chunks := make([]IndexedChunk, 0, len(s.chunks))
	for _, c := range s.chunks {
		chunks = append(chunks, c)
	}

	data, err := json.Marshal(chunks)
	if err != nil {
		return fmt.Errorf("encoding index: %w", err)
	}

	path := filepath.Join(s.dir, "index.json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming index: %w", err)
	}

	s.dirty = false
	return nil
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
