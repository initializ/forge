package llm

import "context"

// Client is the interface for interacting with an LLM provider.
type Client interface {
	// Chat sends a chat completion request and returns the response.
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// ChatStream sends a streaming chat request and returns a channel of deltas.
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamDelta, error)
	// ModelID returns the model identifier this client is configured for.
	ModelID() string
}

// ClientConfig holds configuration for creating an LLM client.
type ClientConfig struct {
	APIKey     string
	BaseURL    string
	Model      string
	OrgID      string
	MaxRetries int
	TimeoutSecs int
}
