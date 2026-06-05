package runtime

import (
	"net/http"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Regression tests for issue #87 / FWS-3 — X-Forge-* response header
// emission. Headers are the inline channel for orchestrator real-time
// cost enforcement; they populate regardless of whether OTel tracing
// is enabled.

func TestApplyForgeUsageHeaders_StampsAllFields(t *testing.T) {
	h := http.Header{}
	applyForgeUsageHeaders(h, coreruntime.LLMUsageSnapshot{
		InputTokens:        450,
		OutputTokens:       180,
		InvocationDuration: 1234 * time.Millisecond,
		PrimaryModel:       "claude-sonnet-4-6",
		PrimaryProvider:    "anthropic",
		LLMCallCount:       3,
	})

	if h.Get(HeaderForgeTokensIn) != "450" {
		t.Errorf("X-Forge-Tokens-In = %q, want 450", h.Get(HeaderForgeTokensIn))
	}
	if h.Get(HeaderForgeTokensOut) != "180" {
		t.Errorf("X-Forge-Tokens-Out = %q, want 180", h.Get(HeaderForgeTokensOut))
	}
	if h.Get(HeaderForgeDurationMs) != "1234" {
		t.Errorf("X-Forge-Duration-Ms = %q, want 1234", h.Get(HeaderForgeDurationMs))
	}
	if h.Get(HeaderForgeModel) != "claude-sonnet-4-6" {
		t.Errorf("X-Forge-Model = %q, want claude-sonnet-4-6", h.Get(HeaderForgeModel))
	}
	if h.Get(HeaderForgeProvider) != "anthropic" {
		t.Errorf("X-Forge-Provider = %q, want anthropic", h.Get(HeaderForgeProvider))
	}
}

func TestApplyForgeUsageHeaders_NoLLMCalls_StillStampsDuration(t *testing.T) {
	// Short-circuited invocation (guardrail-failed before LLM dispatch):
	// orchestrator still wants a wall-clock figure, but token fields
	// would mislead — emit duration only.
	h := http.Header{}
	applyForgeUsageHeaders(h, coreruntime.LLMUsageSnapshot{
		InvocationDuration: 5 * time.Millisecond,
		LLMCallCount:       0,
	})

	if h.Get(HeaderForgeDurationMs) != "5" {
		t.Errorf("X-Forge-Duration-Ms must still be stamped on short-circuited invocations, got %q", h.Get(HeaderForgeDurationMs))
	}
	if h.Get(HeaderForgeTokensIn) != "" || h.Get(HeaderForgeTokensOut) != "" {
		t.Errorf("token headers must NOT be stamped when no LLM calls happened, got in=%q out=%q",
			h.Get(HeaderForgeTokensIn), h.Get(HeaderForgeTokensOut))
	}
}

func TestApplyForgeUsageHeaders_OmitsModelProviderWhenAbsent(t *testing.T) {
	// Edge case: LLM call happened but provider/model were empty (no
	// runtime attribution available). Stamp tokens + duration only —
	// don't stamp empty model/provider values.
	h := http.Header{}
	applyForgeUsageHeaders(h, coreruntime.LLMUsageSnapshot{
		InputTokens:        50,
		OutputTokens:       25,
		InvocationDuration: 100 * time.Millisecond,
		LLMCallCount:       1,
	})
	if _, present := h[http.CanonicalHeaderKey(HeaderForgeModel)]; present {
		t.Errorf("X-Forge-Model must be omitted when PrimaryModel is empty")
	}
	if _, present := h[http.CanonicalHeaderKey(HeaderForgeProvider)]; present {
		t.Errorf("X-Forge-Provider must be omitted when PrimaryProvider is empty")
	}
}
