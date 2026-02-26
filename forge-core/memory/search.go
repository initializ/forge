package memory

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// SearchConfig configures the hybrid search engine.
type SearchConfig struct {
	VectorWeight  float64       // weight for vector similarity (default: 0.7)
	KeywordWeight float64       // weight for keyword overlap (default: 0.3)
	DecayHalfLife time.Duration // temporal decay half-life (default: 7 days)
	DecayEnabled  bool          // whether to apply temporal decay (default: true)
	TopK          int           // max results to return (default: 10)
}

// DefaultSearchConfig returns a SearchConfig with sensible defaults.
func DefaultSearchConfig() SearchConfig {
	return SearchConfig{
		VectorWeight:  0.7,
		KeywordWeight: 0.3,
		DecayHalfLife: 7 * 24 * time.Hour,
		DecayEnabled:  true,
		TopK:          10,
	}
}

// HybridSearcher combines vector similarity, keyword overlap, and temporal
// decay for memory retrieval.
type HybridSearcher struct {
	store    VectorStore
	embedder llm.Embedder // nil = keyword-only mode
	config   SearchConfig
}

// NewHybridSearcher creates a new hybrid searcher.
func NewHybridSearcher(store VectorStore, embedder llm.Embedder, config SearchConfig) *HybridSearcher {
	if config.TopK <= 0 {
		config.TopK = 10
	}
	if config.VectorWeight <= 0 {
		config.VectorWeight = 0.7
	}
	if config.KeywordWeight <= 0 {
		config.KeywordWeight = 0.3
	}
	if config.DecayHalfLife <= 0 {
		config.DecayHalfLife = 7 * 24 * time.Hour
	}
	return &HybridSearcher{
		store:    store,
		embedder: embedder,
		config:   config,
	}
}

// Search performs hybrid search: vector + keyword + temporal decay.
// If no embedder is available, falls back to keyword-only search over all chunks.
func (h *HybridSearcher) Search(ctx context.Context, query string) ([]SearchResult, error) {
	candidateK := h.config.TopK * 3 // fetch more candidates for re-ranking

	var candidates []SearchResult

	if h.embedder != nil {
		// Vector search path
		resp, err := h.embedder.Embed(ctx, &llm.EmbeddingRequest{Texts: []string{query}})
		if err != nil {
			// Fall back to keyword-only on embedding failure.
			return h.keywordOnlySearch(ctx, query)
		}
		if len(resp.Embeddings) == 0 {
			return h.keywordOnlySearch(ctx, query)
		}

		candidates, err = h.store.Search(ctx, resp.Embeddings[0], candidateK)
		if err != nil {
			return nil, err
		}
	} else {
		// No embedder — keyword-only mode. Fetch all chunks.
		candidates, _ = h.store.Search(ctx, nil, 0)
		if len(candidates) == 0 {
			return nil, nil
		}
	}

	// Re-rank with keyword scoring and temporal decay.
	queryTerms := tokenize(query)
	now := time.Now().UTC()

	type scoredResult struct {
		result     SearchResult
		finalScore float64
	}

	scored := make([]scoredResult, 0, len(candidates))
	for _, c := range candidates {
		vectorScore := c.Score
		keywordScore := keywordOverlap(queryTerms, c.Chunk.Content)

		// Temporal decay: MEMORY.md is evergreen (decay = 1.0).
		decay := 1.0
		if h.config.DecayEnabled && c.Chunk.Source != "MEMORY.md" {
			age := now.Sub(c.Chunk.CreatedAt)
			decay = math.Exp(-math.Ln2 / h.config.DecayHalfLife.Seconds() * age.Seconds())
		}

		var finalScore float64
		if h.embedder != nil {
			finalScore = (h.config.VectorWeight*vectorScore + h.config.KeywordWeight*keywordScore) * decay
		} else {
			finalScore = keywordScore * decay
		}

		// Skip zero-score results (irrelevant in keyword-only mode).
		if finalScore == 0 {
			continue
		}

		scored = append(scored, scoredResult{
			result:     SearchResult{Chunk: c.Chunk, Score: finalScore},
			finalScore: finalScore,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].finalScore > scored[j].finalScore
	})

	topK := min(h.config.TopK, len(scored))

	results := make([]SearchResult, topK)
	for i := range topK {
		results[i] = scored[i].result
	}
	return results, nil
}

// keywordOnlySearch loads all chunks and ranks by keyword overlap + decay.
func (h *HybridSearcher) keywordOnlySearch(ctx context.Context, query string) ([]SearchResult, error) {
	// Use a nil vector with k=0 to get all chunks (store should return everything).
	all, err := h.store.Search(ctx, nil, 0)
	if err != nil {
		return nil, err
	}

	queryTerms := tokenize(query)
	now := time.Now().UTC()

	type scoredResult struct {
		result     SearchResult
		finalScore float64
	}

	scored := make([]scoredResult, 0, len(all))
	for _, c := range all {
		kw := keywordOverlap(queryTerms, c.Chunk.Content)
		if kw == 0 {
			continue
		}

		decay := 1.0
		if h.config.DecayEnabled && c.Chunk.Source != "MEMORY.md" {
			age := now.Sub(c.Chunk.CreatedAt)
			decay = math.Exp(-math.Ln2 / h.config.DecayHalfLife.Seconds() * age.Seconds())
		}

		finalScore := kw * decay
		scored = append(scored, scoredResult{
			result:     SearchResult{Chunk: c.Chunk, Score: finalScore},
			finalScore: finalScore,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].finalScore > scored[j].finalScore
	})

	topK := min(h.config.TopK, len(scored))

	results := make([]SearchResult, topK)
	for i := range topK {
		results[i] = scored[i].result
	}
	return results, nil
}

// tokenize splits text into lowercase terms for keyword matching.
func tokenize(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	// Deduplicate
	seen := make(map[string]struct{}, len(words))
	unique := make([]string, 0, len(words))
	for _, w := range words {
		// Strip common punctuation
		w = strings.Trim(w, ".,;:!?\"'()[]{}—-")
		if w == "" {
			continue
		}
		if _, ok := seen[w]; !ok {
			seen[w] = struct{}{}
			unique = append(unique, w)
		}
	}
	return unique
}

// keywordOverlap computes the fraction of query terms found in the content.
func keywordOverlap(queryTerms []string, content string) float64 {
	if len(queryTerms) == 0 {
		return 0
	}
	lower := strings.ToLower(content)
	matched := 0
	for _, term := range queryTerms {
		if strings.Contains(lower, term) {
			matched++
		}
	}
	return float64(matched) / float64(len(queryTerms))
}
