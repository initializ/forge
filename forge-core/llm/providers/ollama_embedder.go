package providers

// OllamaEmbedder wraps OpenAIEmbedder with Ollama-specific defaults.
// Ollama provides an OpenAI-compatible /v1/embeddings endpoint.
type OllamaEmbedder struct {
	*OpenAIEmbedder
}

const (
	ollamaDefaultEmbeddingModel = "nomic-embed-text"
	ollamaDefaultEmbeddingDims  = 768
)

// NewOllamaEmbedder creates an embedder that talks to a local Ollama server.
func NewOllamaEmbedder(cfg OpenAIEmbedderConfig) *OllamaEmbedder {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434/v1"
	}
	if cfg.APIKey == "" {
		cfg.APIKey = "ollama" // Ollama requires a non-empty key
	}
	if cfg.Model == "" {
		cfg.Model = ollamaDefaultEmbeddingModel
	}
	if cfg.Dims <= 0 {
		cfg.Dims = ollamaDefaultEmbeddingDims
	}
	return &OllamaEmbedder{
		OpenAIEmbedder: NewOpenAIEmbedder(cfg),
	}
}
