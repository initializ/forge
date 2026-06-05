package runtime

import (
	"net/http"
	"strconv"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// A2A response header names for per-invocation cost telemetry. These
// are the inline channel for orchestrator real-time cost enforcement
// during parallel workflow execution — the orchestrator can ceiling-check
// against running totals before the next stage dispatches. They populate
// regardless of whether OTel tracing is enabled. See issue #87 / FWS-3.
const (
	HeaderForgeTokensIn   = "X-Forge-Tokens-In"
	HeaderForgeTokensOut  = "X-Forge-Tokens-Out"
	HeaderForgeDurationMs = "X-Forge-Duration-Ms"
	HeaderForgeModel      = "X-Forge-Model"
	HeaderForgeProvider   = "X-Forge-Provider"
)

// applyForgeUsageHeaders stamps the X-Forge-* invocation-usage headers
// onto the given http.Header from a usage snapshot. Headers are omitted
// for snapshots with zero LLM calls (e.g. guardrail-failed invocations
// that never reached the LLM) so the response shape mirrors what
// actually happened.
func applyForgeUsageHeaders(h http.Header, snap coreruntime.LLMUsageSnapshot) {
	if snap.LLMCallCount == 0 {
		// Still stamp duration so orchestrators always see a wall-clock
		// figure even for short-circuited invocations.
		h.Set(HeaderForgeDurationMs, strconv.FormatInt(snap.InvocationDuration.Milliseconds(), 10))
		return
	}
	h.Set(HeaderForgeTokensIn, strconv.Itoa(snap.InputTokens))
	h.Set(HeaderForgeTokensOut, strconv.Itoa(snap.OutputTokens))
	h.Set(HeaderForgeDurationMs, strconv.FormatInt(snap.InvocationDuration.Milliseconds(), 10))
	if snap.PrimaryModel != "" {
		h.Set(HeaderForgeModel, snap.PrimaryModel)
	}
	if snap.PrimaryProvider != "" {
		h.Set(HeaderForgeProvider, snap.PrimaryProvider)
	}
}
