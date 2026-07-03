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
	APIKey      string
	BaseURL     string
	Model       string
	OrgID       string
	MaxRetries  int
	TimeoutSecs int

	// AuthScheme + AWSRegion control outbound authentication when
	// the operator points the client at AWS Bedrock (Anthropic
	// passthrough or OpenAI compatibility endpoint) or any other
	// SigV4-fronted gateway. Issue #202 Phase 2. Mirrors the
	// matching forge.yaml ModelRef fields.
	//
	// AuthScheme == "" preserves the pre-#202 behavior — the
	// Anthropic client sets `x-api-key: <APIKey>`, the OpenAI
	// client sets `Authorization: Bearer <APIKey>`. AuthScheme ==
	// "aws_sigv4" wraps the client's transport with the SigV4
	// signer and skips the native header logic; APIKey is ignored.
	AuthScheme string
	AWSRegion  string

	// PromptCaching opts the provider client into injecting the
	// provider's prompt-cache primitives on every request:
	//
	//   - anthropic: a cache_control ephemeral breakpoint on the last
	//     tool definition and on the system prompt block, caching the
	//     stable tools+system prefix across turns. Also honored by
	//     Anthropic-on-Bedrock gateways (aws_sigv4), which speak the
	//     same wire format.
	//   - openai: a stable prompt_cache_key derived from
	//     (model, system, tool names), pinning cache routing for the
	//     session. OpenAI prefix caching itself is automatic ≥1024
	//     tokens; the key improves hit locality.
	//
	// Off by default — wire formats stay byte-identical to the
	// pre-compression contract unless the operator opts in
	// (compression.cache_hints / compression.enabled in forge.yaml).
	PromptCaching bool
}
