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

func TestApplyForgeUsageHeaders_TokensIn_BillsFromTrueTotalUnderCaching(t *testing.T) {
	// Issue #431: under Anthropic prompt caching, InputTokens is only the
	// uncached delta. X-Forge-Tokens-In must carry TotalInputTokens (delta
	// + cache read + cache creation) so an orchestrator ceiling-checking
	// against the header can't be fooled into letting a cache-heavy stage
	// past a cost cap.
	h := http.Header{}
	applyForgeUsageHeaders(h, coreruntime.LLMUsageSnapshot{
		InputTokens:      32,   // summed uncached delta
		TotalInputTokens: 8032, // delta + 8000 cached prefix
		OutputTokens:     180,
		LLMCallCount:     2,
	})
	if h.Get(HeaderForgeTokensIn) != "8032" {
		t.Errorf("X-Forge-Tokens-In = %q, want 8032 (true total incl. cache), not the uncached delta", h.Get(HeaderForgeTokensIn))
	}
	if h.Get(HeaderForgeTokensOut) != "180" {
		t.Errorf("X-Forge-Tokens-Out = %q, want 180", h.Get(HeaderForgeTokensOut))
	}
}

func TestApplyForgeUsageHeaders_TokensIn_NeverBelowUncachedDelta(t *testing.T) {
	// Defensive: a snapshot with TotalInputTokens unpopulated (0) but a
	// real InputTokens must still report the delta, never a smaller
	// number — mirrors the `total_input_tokens ?? input_tokens` fallback.
	h := http.Header{}
	applyForgeUsageHeaders(h, coreruntime.LLMUsageSnapshot{
		InputTokens:  450,
		OutputTokens: 180,
		LLMCallCount: 1,
	})
	if h.Get(HeaderForgeTokensIn) != "450" {
		t.Errorf("X-Forge-Tokens-In = %q, want 450 (falls back to InputTokens when total is unset)", h.Get(HeaderForgeTokensIn))
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
