package providers

import (
	"testing"
)

func TestNewEmbedder(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantErr  bool
	}{
		{"openai", "openai", false},
		{"gemini", "gemini", false},
		{"ollama", "ollama", false},
		{"anthropic", "anthropic", true},
		{"unknown", "unknown", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OpenAIEmbedderConfig{APIKey: "test-key"}
			emb, err := NewEmbedder(tt.provider, cfg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for provider %q", tt.provider)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if emb == nil {
				t.Fatal("expected non-nil embedder")
			}
			if emb.Dimensions() <= 0 {
				t.Errorf("expected positive dimensions, got %d", emb.Dimensions())
			}
		})
	}
}

func TestOpenAIEmbedderDefaults(t *testing.T) {
	emb := NewOpenAIEmbedder(OpenAIEmbedderConfig{})
	if emb.Dimensions() != defaultEmbeddingDims {
		t.Errorf("expected %d dims, got %d", defaultEmbeddingDims, emb.Dimensions())
	}
	if emb.model != defaultEmbeddingModel {
		t.Errorf("expected model %q, got %q", defaultEmbeddingModel, emb.model)
	}
}

func TestOllamaEmbedderDefaults(t *testing.T) {
	emb := NewOllamaEmbedder(OpenAIEmbedderConfig{})
	if emb.Dimensions() != ollamaDefaultEmbeddingDims {
		t.Errorf("expected %d dims, got %d", ollamaDefaultEmbeddingDims, emb.Dimensions())
	}
	if emb.model != ollamaDefaultEmbeddingModel {
		t.Errorf("expected model %q, got %q", ollamaDefaultEmbeddingModel, emb.model)
	}
}
