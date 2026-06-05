package runtime

import (
	"context"
	"sync"
	"time"
)

// LLMUsageAccumulator aggregates per-invocation LLM usage so the A2A
// response handler can populate X-Forge-Tokens-In / X-Forge-Tokens-Out
// / X-Forge-Duration-Ms / X-Forge-Model / X-Forge-Provider headers.
//
// One accumulator is created per A2A invocation by the runner and
// stashed in context.Context. Every AfterLLMCall hook calls AddLLMCall
// to fold the current call's counts into the running totals. At
// response time the runner reads Snapshot() and stamps the headers.
//
// Headers are the orchestration channel for real-time cost enforcement
// during parallel workflow execution. They populate regardless of
// whether OTel tracing is enabled — they're the orchestration channel,
// not the observability channel. See issue #87 / FWS-3.
type LLMUsageAccumulator struct {
	mu               sync.Mutex
	invocationStart  time.Time
	inputTokensSum   int
	outputTokensSum  int
	llmTimeSum       time.Duration
	primaryModel     string
	primaryProvider  string
	llmCallCount     int
	tokensUnavailHit bool
}

// NewLLMUsageAccumulator returns a fresh accumulator with its invocation
// clock started at the time of the call.
func NewLLMUsageAccumulator() *LLMUsageAccumulator {
	return &LLMUsageAccumulator{invocationStart: time.Now()}
}

// AddLLMCall folds one LLM call's usage + duration into the running
// totals. The most-recently-added call's model + provider become the
// "primary" reported in the X-Forge-Model / X-Forge-Provider headers,
// matching the issue's spec: "the primary model used (most recent if
// multiple)".
func (a *LLMUsageAccumulator) AddLLMCall(model, provider string, usage LLMUsage, duration time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inputTokensSum += usage.InputTokens
	a.outputTokensSum += usage.OutputTokens
	a.llmTimeSum += duration
	a.llmCallCount++
	if model != "" {
		a.primaryModel = model
	}
	if provider != "" {
		a.primaryProvider = provider
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		a.tokensUnavailHit = true
	}
}

// LLMUsageSnapshot is an immutable readout of the accumulator's totals
// at a single point in time. Returned by Snapshot for use by the A2A
// response handler.
type LLMUsageSnapshot struct {
	InputTokens        int
	OutputTokens       int
	LLMTimeTotal       time.Duration // sum of per-LLM-call durations
	InvocationDuration time.Duration // wall-clock since accumulator creation
	PrimaryModel       string
	PrimaryProvider    string
	LLMCallCount       int
	TokensUnavailable  bool
}

// Snapshot returns the current totals. Safe to call from a goroutine
// different from AddLLMCall callers.
func (a *LLMUsageAccumulator) Snapshot() LLMUsageSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return LLMUsageSnapshot{
		InputTokens:        a.inputTokensSum,
		OutputTokens:       a.outputTokensSum,
		LLMTimeTotal:       a.llmTimeSum,
		InvocationDuration: time.Since(a.invocationStart),
		PrimaryModel:       a.primaryModel,
		PrimaryProvider:    a.primaryProvider,
		LLMCallCount:       a.llmCallCount,
		TokensUnavailable:  a.tokensUnavailHit && a.inputTokensSum == 0 && a.outputTokensSum == 0,
	}
}

type llmUsageAccumulatorKey struct{}

// WithLLMUsageAccumulator stashes a per-invocation accumulator in ctx.
// The runner creates one per A2A invocation at request entry; the
// AfterLLMCall hook reads it via LLMUsageAccumulatorFromContext and
// folds each call's counts into the totals.
func WithLLMUsageAccumulator(ctx context.Context, acc *LLMUsageAccumulator) context.Context {
	return context.WithValue(ctx, llmUsageAccumulatorKey{}, acc)
}

// LLMUsageAccumulatorFromContext returns the per-invocation
// accumulator from ctx, or nil when no accumulator was attached
// (e.g. internal cron-fire paths that don't need response headers).
func LLMUsageAccumulatorFromContext(ctx context.Context) *LLMUsageAccumulator {
	if acc, ok := ctx.Value(llmUsageAccumulatorKey{}).(*LLMUsageAccumulator); ok {
		return acc
	}
	return nil
}
