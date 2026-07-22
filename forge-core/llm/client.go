package llm

import "context"

// Outbound LLM auth schemes (ClientConfig.AuthScheme / ModelRef.auth_scheme).
const (
	// AuthSchemeAWSSigV4 signs every outbound request with AWS SigV4
	// and skips the provider-native API-key header (issue #202 Phase 2).
	AuthSchemeAWSSigV4 = "aws_sigv4"

	// AuthSchemeAPIKeyHeader additionally sends the API key in a gateway
	// header (default `apikey`, overridable via AuthHeaderName) ON TOP OF
	// the provider-native header. For API gateways whose auth plugin reads
	// a fixed header name — e.g. Kong AI Gateway's key-auth, which reads
	// `apikey` and ignores `Authorization` / `x-api-key`. Additive, so it
	// is safe to enable against non-gateway endpoints too (issue #302).
	//
	// Use this when the gateway either replaces the native header upstream
	// (Kong request-transformer `replace`, or ai-proxy with
	// allow_override=true) or the key is itself a valid provider key that
	// passes through.
	AuthSchemeAPIKeyHeader = "apikey_header"

	// AuthSchemeAPIKeyHeaderOnly sends the API key ONLY in the gateway
	// header and SUPPRESSES the provider-native header (`x-api-key` /
	// `Authorization`), mirroring how aws_sigv4 skips the native header.
	// The gateway owns provider auth: it must inject the real upstream
	// credential itself. Use this when the gateway ADDS the native header
	// (Kong request-transformer `add`, which won't overwrite an existing
	// one) — sending the native header from Forge would block that
	// injection and the provider would 401 on the gateway key (issue #349
	// follow-up: Kong `add`-injection). The gateway key never reaches the
	// provider's native auth header.
	AuthSchemeAPIKeyHeaderOnly = "apikey_header_only"

	// DefaultAPIKeyHeaderName is the header the apikey_header schemes use
	// when AuthHeaderName is unset — Kong key-auth's default key_names.
	DefaultAPIKeyHeaderName = "apikey"
)

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
	// AuthScheme == "apikey_header" ADDITIONALLY sends APIKey in the
	// AuthHeaderName header (default "apikey") alongside the native
	// header, for gateways like Kong that read a fixed key header
	// (issue #302). AuthScheme == "apikey_header_only" sends APIKey in the
	// gateway header but SUPPRESSES the native header, for gateways that
	// inject the real upstream credential themselves.
	AuthScheme string
	AWSRegion  string

	// AuthHeaderName overrides the header used by the "apikey_header"
	// scheme. Empty → DefaultAPIKeyHeaderName ("apikey"). Ignored for
	// every other scheme. Set it for a gateway with custom key_names,
	// e.g. "x-gateway-key". Issue #302.
	AuthHeaderName string

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
