package providers

import (
	"net/http"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// TestAnthropicClient_DefaultAuthSchemeKeepsXAPIKey pins the
// pre-#202 contract: an AnthropicClient with no AuthScheme set sends
// the `x-api-key` and `anthropic-version` headers exactly as it
// always has. Without this every existing Anthropic deployment
// breaks the moment Phase 2 lands.
func TestAnthropicClient_DefaultAuthSchemeKeepsXAPIKey(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{
		APIKey: "sk-ant-test",
		Model:  "claude-test",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	c.setHeaders(req)

	if got := req.Header.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("default path lost x-api-key; got %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version missing on default path: %q", got)
	}
}

// TestAnthropicClient_SigV4AuthSchemeOmitsXAPIKey is the Phase 2
// invariant: when the client is configured for SigV4 outbound, the
// per-request x-api-key header MUST NOT be sent. The SigV4 transport
// will write Authorization instead — pre-stamping x-api-key would
// just confuse the upstream signature verifier and the proxy trace
// logs.
func TestAnthropicClient_SigV4AuthSchemeOmitsXAPIKey(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{
		APIKey:     "sk-ant-test",
		Model:      "anthropic.claude-sonnet-4-20250514-v1:0",
		AuthScheme: "aws_sigv4",
		AWSRegion:  "us-east-1",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/x", nil)
	c.setHeaders(req)

	if got := req.Header.Get("x-api-key"); got != "" {
		t.Errorf("aws_sigv4 path leaked x-api-key: %q", got)
	}
	// anthropic-version must still ride — Bedrock's Anthropic
	// passthrough recognizes it. SigV4 only swaps the auth
	// mechanism, not the wire envelope.
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version dropped on aws_sigv4 path: %q", got)
	}
}

// TestAnthropicClient_SigV4WrapsTransport confirms the client's
// http.Client.Transport is a *SigV4Transport when AuthScheme=aws_sigv4,
// so every outbound request flows through the signer. Without this
// the auth header would be skipped (above test) AND the transport
// would be unsigned — leaving requests with no auth at all.
func TestAnthropicClient_SigV4WrapsTransport(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{
		AuthScheme: "aws_sigv4",
		AWSRegion:  "us-east-1",
	})
	if _, ok := c.client.Transport.(*SigV4Transport); !ok {
		t.Errorf("transport is %T, want *SigV4Transport", c.client.Transport)
	}
}

// TestAnthropicClient_DefaultTransportNotWrapped pins the no-overhead
// path: a default AnthropicClient has http.Client.Transport == nil
// (which net/http falls back to DefaultTransport). Wrapping when
// AuthScheme is unset would impose a per-request SigV4 attempt + env
// lookup on every Anthropic call that never needed it.
func TestAnthropicClient_DefaultTransportNotWrapped(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{APIKey: "sk-ant-x"})
	if c.client.Transport != nil {
		t.Errorf("default transport should be unwrapped (nil); got %T", c.client.Transport)
	}
}

// TestOpenAIClient_DefaultAuthSchemeKeepsBearer pins the symmetric
// pre-#202 contract for OpenAI: Authorization: Bearer <key> is the
// default and continues to be set.
func TestOpenAIClient_DefaultAuthSchemeKeepsBearer(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey: "sk-test",
		Model:  "gpt-test",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("default path lost Authorization: %q", got)
	}
}

// TestOpenAIClient_SigV4AuthSchemeOmitsBearer is the Phase 2 OpenAI
// pin: at Bedrock's OpenAI-compat endpoint the SigV4 signer stamps
// Authorization; Forge MUST NOT pre-populate Bearer.
func TestOpenAIClient_SigV4AuthSchemeOmitsBearer(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey:     "sk-test",
		Model:      "openai.gpt-4o",
		AuthScheme: "aws_sigv4",
		AWSRegion:  "us-east-1",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("aws_sigv4 path leaked Authorization: %q", got)
	}
}

// TestOpenAIClient_SigV4WrapsTransport mirrors the Anthropic side —
// confirms the SigV4Transport is in place on the client's http.Client.
func TestOpenAIClient_SigV4WrapsTransport(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		AuthScheme: "aws_sigv4",
		AWSRegion:  "us-east-1",
	})
	if _, ok := c.client.Transport.(*SigV4Transport); !ok {
		t.Errorf("transport is %T, want *SigV4Transport", c.client.Transport)
	}
}

// TestOpenAIClient_OrgIDStillSetUnderSigV4 confirms the
// OpenAI-Organization header isn't accidentally suppressed under the
// SigV4 path. An operator pointing a Forge agent with an org ID at
// Bedrock's OpenAI compat layer expects the same multi-tenant
// attribution to surface as the standard provider would emit.
func TestOpenAIClient_OrgIDStillSetUnderSigV4(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		AuthScheme: "aws_sigv4",
		AWSRegion:  "us-east-1",
		OrgID:      "org-xyz",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/v1/chat/completions", nil)
	c.setHeaders(req)
	if got := req.Header.Get("OpenAI-Organization"); got != "org-xyz" {
		t.Errorf("OpenAI-Organization dropped under aws_sigv4: %q", got)
	}
}

// --- issue #302: apikey_header gateway scheme -----------------------------

// TestAnthropicClient_APIKeyHeaderSendsBothHeaders is the #302 invariant on
// the Anthropic side: apikey_header is ADDITIVE — the native x-api-key still
// rides (Kong's ai-proxy replaces/injects the upstream provider header) AND
// the gateway's `apikey` header carries the key so Kong key-auth admits the
// request.
func TestAnthropicClient_APIKeyHeaderSendsBothHeaders(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{
		APIKey:     "sk-ant-test",
		Model:      "claude-test",
		AuthScheme: llm.AuthSchemeAPIKeyHeader,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/messages", nil)
	c.setHeaders(req)

	if got := req.Header.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("apikey_header dropped native x-api-key: %q", got)
	}
	if got := req.Header.Get("apikey"); got != "sk-ant-test" {
		t.Errorf("apikey_header did not set the gateway apikey header: %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version dropped under apikey_header: %q", got)
	}
}

// TestOpenAIClient_APIKeyHeaderSendsBothHeaders mirrors the above for OpenAI:
// Authorization: Bearer still rides alongside the gateway apikey header.
func TestOpenAIClient_APIKeyHeaderSendsBothHeaders(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey:     "sk-test",
		Model:      "gpt-test",
		AuthScheme: llm.AuthSchemeAPIKeyHeader,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("apikey_header dropped native Authorization: %q", got)
	}
	if got := req.Header.Get("apikey"); got != "sk-test" {
		t.Errorf("apikey_header did not set the gateway apikey header: %q", got)
	}
}

// TestAPIKeyHeaderScheme_CustomHeaderName pins the auth_header_name override
// for gateways with non-default key_names (#302).
func TestAPIKeyHeaderScheme_CustomHeaderName(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey:         "sk-test",
		Model:          "gpt-test",
		AuthScheme:     llm.AuthSchemeAPIKeyHeader,
		AuthHeaderName: "x-gateway-key",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("x-gateway-key"); got != "sk-test" {
		t.Errorf("custom auth_header_name not honored: %q", got)
	}
	if got := req.Header.Get("apikey"); got != "" {
		t.Errorf("default apikey header should not be set when a custom name is given: %q", got)
	}
}

// TestAPIKeyHeaderScheme_NoopOffPath confirms the scheme is inert everywhere
// it should be: the default (unset) scheme never emits the gateway header,
// and an empty APIKey emits nothing even under apikey_header.
func TestAPIKeyHeaderScheme_NoopOffPath(t *testing.T) {
	// Default scheme → no apikey header.
	def := NewOpenAIClient(llm.ClientConfig{APIKey: "sk-test", Model: "gpt-test"})
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	def.setHeaders(req)
	if got := req.Header.Get("apikey"); got != "" {
		t.Errorf("default scheme leaked an apikey header: %q", got)
	}

	// apikey_header but empty key → nothing to send.
	empty := NewOpenAIClient(llm.ClientConfig{Model: "gpt-test", AuthScheme: llm.AuthSchemeAPIKeyHeader})
	req2, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/chat/completions", nil)
	empty.setHeaders(req2)
	if got := req2.Header.Get("apikey"); got != "" {
		t.Errorf("empty APIKey should not set an apikey header: %q", got)
	}
}

// --- apikey_header_only: gateway header, native suppressed ----------------

// TestAnthropicClient_APIKeyHeaderOnlySuppressesXAPIKey pins the new scheme on
// the Anthropic side: the gateway `apikey` header carries the key and the
// native x-api-key is NOT sent, so Forge's gateway key never reaches Anthropic
// (the gateway injects the real upstream key). anthropic-version still rides.
func TestAnthropicClient_APIKeyHeaderOnlySuppressesXAPIKey(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{
		APIKey:     "kong-consumer-key",
		Model:      "claude-test",
		AuthScheme: llm.AuthSchemeAPIKeyHeaderOnly,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/messages", nil)
	c.setHeaders(req)

	if got := req.Header.Get("x-api-key"); got != "" {
		t.Errorf("apikey_header_only leaked native x-api-key: %q", got)
	}
	if got := req.Header.Get("apikey"); got != "kong-consumer-key" {
		t.Errorf("apikey_header_only did not set the gateway header: %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version dropped under apikey_header_only: %q", got)
	}
}

// TestOpenAIClient_APIKeyHeaderOnlySuppressesBearer mirrors the above for
// OpenAI: the gateway header carries the key, Authorization: Bearer is absent.
func TestOpenAIClient_APIKeyHeaderOnlySuppressesBearer(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey:     "kong-consumer-key",
		Model:      "gpt-test",
		AuthScheme: llm.AuthSchemeAPIKeyHeaderOnly,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("apikey_header_only leaked native Authorization: %q", got)
	}
	if got := req.Header.Get("apikey"); got != "kong-consumer-key" {
		t.Errorf("apikey_header_only did not set the gateway header: %q", got)
	}
}

// TestAPIKeyHeaderOnly_CustomHeaderName confirms auth_header_name is honored
// under the suppress-native scheme too.
func TestAPIKeyHeaderOnly_CustomHeaderName(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey:         "kong-key",
		Model:          "gpt-test",
		AuthScheme:     llm.AuthSchemeAPIKeyHeaderOnly,
		AuthHeaderName: "x-gateway-key",
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("x-gateway-key"); got != "kong-key" {
		t.Errorf("custom auth_header_name not honored under apikey_header_only: %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("native Authorization must stay suppressed: %q", got)
	}
}

// TestAPIKeyHeaderScheme_NeverClobbersNativeHeader is the defense-in-depth
// guard (#303 review): even if a ClientConfig sets auth_header_name to a
// native auth header (which `forge validate` rejects), the helper must NOT
// overwrite the provider's Bearer token with the raw key.
func TestAPIKeyHeaderScheme_NeverClobbersNativeHeader(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{
		APIKey:         "sk-test",
		Model:          "gpt-test",
		AuthScheme:     llm.AuthSchemeAPIKeyHeader,
		AuthHeaderName: "authorization", // lowercase — must still be caught
	})
	req, _ := http.NewRequest(http.MethodPost, "https://kong.example/v1/chat/completions", nil)
	c.setHeaders(req)

	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("native Authorization was clobbered by the gateway header: %q", got)
	}
}
