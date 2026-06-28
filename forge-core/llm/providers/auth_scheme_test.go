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
