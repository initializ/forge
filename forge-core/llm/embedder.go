package llm

import "context"

// EmbeddingRequest is a provider-agnostic request to generate embeddings.
type EmbeddingRequest struct {
	Texts []string // texts to embed
	Model string   // optional model override
}

// EmbeddingResponse is a provider-agnostic embedding response.
type EmbeddingResponse struct {
	Embeddings [][]float32
	Model      string
	Usage      UsageInfo
}

// Embedder generates vector embeddings from text.
type Embedder interface {
	// Embed produces embeddings for the given texts.
	Embed(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)
	// Dimensions returns the dimensionality of the embedding vectors.
	Dimensions() int
}
