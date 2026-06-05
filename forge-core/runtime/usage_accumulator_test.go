package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Regression tests for issue #87 / FWS-3 — the per-invocation LLM
// usage accumulator. Tracks running totals so the A2A response handler
// can stamp X-Forge-Tokens-In/Out/Duration-Ms/Model/Provider headers
// and emit invocation_complete with aggregated counts.

func TestLLMUsageAccumulator_AggregatesAcrossCalls(t *testing.T) {
	acc := NewLLMUsageAccumulator()
	acc.AddLLMCall("claude", "anthropic", LLMUsage{InputTokens: 100, OutputTokens: 50}, 50*time.Millisecond)
	acc.AddLLMCall("claude", "anthropic", LLMUsage{InputTokens: 200, OutputTokens: 75}, 80*time.Millisecond)
	acc.AddLLMCall("claude", "anthropic", LLMUsage{InputTokens: 50, OutputTokens: 25}, 30*time.Millisecond)

	snap := acc.Snapshot()
	if snap.InputTokens != 350 {
		t.Errorf("InputTokens sum = %d, want 350", snap.InputTokens)
	}
	if snap.OutputTokens != 150 {
		t.Errorf("OutputTokens sum = %d, want 150", snap.OutputTokens)
	}
	if snap.LLMCallCount != 3 {
		t.Errorf("LLMCallCount = %d, want 3", snap.LLMCallCount)
	}
}

func TestLLMUsageAccumulator_PrimaryIsMostRecentNonEmpty(t *testing.T) {
	// Spec: X-Forge-Model / X-Forge-Provider report "the primary model
	// used (most recent if multiple)." This matches the most common
	// orchestration pattern where the final model decides cost class.
	acc := NewLLMUsageAccumulator()
	acc.AddLLMCall("claude-haiku", "anthropic", LLMUsage{InputTokens: 10, OutputTokens: 5}, time.Millisecond)
	acc.AddLLMCall("gpt-4", "openai", LLMUsage{InputTokens: 20, OutputTokens: 10}, time.Millisecond)
	snap := acc.Snapshot()
	if snap.PrimaryModel != "gpt-4" || snap.PrimaryProvider != "openai" {
		t.Errorf("Primary should be most-recent (gpt-4 / openai), got %s / %s", snap.PrimaryModel, snap.PrimaryProvider)
	}
}

func TestLLMUsageAccumulator_TokensUnavailableLatchesOnAllZero(t *testing.T) {
	// If every call had no usage info (Ollama on a self-hosted model),
	// the snapshot's TokensUnavailable must be true so the A2A header
	// layer knows to skip X-Forge-Tokens-* (downstream billing must
	// distinguish "we didn't measure" from "you used zero tokens").
	acc := NewLLMUsageAccumulator()
	acc.AddLLMCall("llama3", "ollama", LLMUsage{InputTokens: 0, OutputTokens: 0}, time.Millisecond)
	snap := acc.Snapshot()
	if !snap.TokensUnavailable {
		t.Errorf("all-zero usage must latch TokensUnavailable=true, got %+v", snap)
	}
}

func TestLLMUsageAccumulator_TokensUnavailableClearsWhenAnyCallReports(t *testing.T) {
	// Mixed-provider workflow: if any call reported usage, totals are
	// meaningful and TokensUnavailable should NOT latch — billing can
	// use the snapshot's InputTokens/OutputTokens as the bill-from value.
	acc := NewLLMUsageAccumulator()
	acc.AddLLMCall("llama3", "ollama", LLMUsage{InputTokens: 0, OutputTokens: 0}, time.Millisecond)
	acc.AddLLMCall("claude", "anthropic", LLMUsage{InputTokens: 100, OutputTokens: 50}, time.Millisecond)
	snap := acc.Snapshot()
	if snap.TokensUnavailable {
		t.Errorf("partial-reporting workflow must NOT latch TokensUnavailable, got %+v", snap)
	}
	if snap.InputTokens != 100 || snap.OutputTokens != 50 {
		t.Errorf("billable totals wrong: %+v", snap)
	}
}

func TestLLMUsageAccumulator_InvocationDurationTrackedSeparatelyFromLLMTime(t *testing.T) {
	// LLM time and wall-clock invocation time are different — LLM time
	// is sum-of-Chat-durations, invocation duration is end-to-end wall
	// clock (includes tool execution, guardrails, audit emission).
	acc := NewLLMUsageAccumulator()
	time.Sleep(15 * time.Millisecond)
	acc.AddLLMCall("claude", "anthropic", LLMUsage{InputTokens: 10}, 5*time.Millisecond)
	snap := acc.Snapshot()
	if snap.LLMTimeTotal != 5*time.Millisecond {
		t.Errorf("LLMTimeTotal must be sum of per-call durations, got %v", snap.LLMTimeTotal)
	}
	if snap.InvocationDuration < 15*time.Millisecond {
		t.Errorf("InvocationDuration must be wall-clock since accumulator creation, got %v", snap.InvocationDuration)
	}
}

func TestLLMUsageAccumulatorFromContext_MissingReturnsNil(t *testing.T) {
	if acc := LLMUsageAccumulatorFromContext(context.Background()); acc != nil {
		t.Errorf("missing ctx value should return nil, got %v", acc)
	}
}

func TestLLMUsageAccumulatorFromContext_RoundTrip(t *testing.T) {
	acc := NewLLMUsageAccumulator()
	ctx := WithLLMUsageAccumulator(context.Background(), acc)
	got := LLMUsageAccumulatorFromContext(ctx)
	if got != acc {
		t.Errorf("ctx round-trip should return same accumulator")
	}
}

func TestLLMUsageAccumulator_ConcurrentAddSafe(t *testing.T) {
	// AfterLLMCall hooks may fire from goroutines. The accumulator
	// must be safe to add to concurrently or we'd lose token data to
	// races — silently undercounting cost data is worse than crashing.
	acc := NewLLMUsageAccumulator()
	const goroutines = 50
	const callsEach = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				acc.AddLLMCall("claude", "anthropic", LLMUsage{InputTokens: 1, OutputTokens: 1}, time.Microsecond)
			}
		}()
	}
	wg.Wait()
	snap := acc.Snapshot()
	want := goroutines * callsEach
	if snap.InputTokens != want || snap.OutputTokens != want {
		t.Errorf("concurrent add lost data: in=%d out=%d, want %d each", snap.InputTokens, snap.OutputTokens, want)
	}
	if snap.LLMCallCount != want {
		t.Errorf("LLMCallCount = %d, want %d", snap.LLMCallCount, want)
	}
}
