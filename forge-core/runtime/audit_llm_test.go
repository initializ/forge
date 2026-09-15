package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Regression tests for issue #87 / FWS-3 — LLM call audit emission
// must carry token / duration / model / provider / request_id with
// OTel-aligned field names, distinguish "no tokens reported"
// (TokensUnavailable=true) from "zero tokens reported," and stay
// additive over the pre-FWS-3 AuditEvent shape.

func TestEmitLLMCall_FullUsage(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithCorrelationID(context.Background(), "corr-1")
	ctx = WithTaskID(ctx, "task-1")

	audit.EmitLLMCall(ctx, LLMCallAuditArgs{
		Model:     "claude-sonnet-4-6",
		Provider:  "anthropic",
		RequestID: "msg_abc",
		Usage:     LLMUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
		Duration:  120 * time.Millisecond,
	})

	var evt AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if evt.Event != AuditLLMCall {
		t.Errorf("Event = %q, want %q", evt.Event, AuditLLMCall)
	}
	if evt.CorrelationID != "corr-1" || evt.TaskID != "task-1" {
		t.Errorf("ctx not pulled: %+v", evt)
	}
	if evt.Model != "claude-sonnet-4-6" || evt.Provider != "anthropic" || evt.RequestID != "msg_abc" {
		t.Errorf("attribution missing: %+v", evt)
	}
	if evt.InputTokens == nil || *evt.InputTokens != 100 {
		t.Errorf("InputTokens want 100, got %v", evt.InputTokens)
	}
	if evt.OutputTokens == nil || *evt.OutputTokens != 50 {
		t.Errorf("OutputTokens want 50, got %v", evt.OutputTokens)
	}
	if evt.TokensUnavailable {
		t.Errorf("TokensUnavailable should be false when counts > 0")
	}
	if evt.DurationMs == nil || *evt.DurationMs != 120 {
		t.Errorf("DurationMs want 120, got %v", evt.DurationMs)
	}
}

func TestEmitLLMCall_CacheTokens_EmitsBreakdownAndSummedTotalInput(t *testing.T) {
	// Issue #431: under Anthropic prompt caching, input_tokens is only
	// the uncached delta. The llm_call event must carry the cache
	// breakdown AND a summed total_input_tokens so a consumer reading
	// total_input_tokens alone can't undercount.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "claude-sonnet-4-6",
		Provider: "anthropic",
		Usage: LLMUsage{
			InputTokens:              12,
			OutputTokens:             8,
			CacheReadInputTokens:     4000,
			CacheCreationInputTokens: 200,
		},
		Duration: 10 * time.Millisecond,
	})

	var evt AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if evt.InputTokens == nil || *evt.InputTokens != 12 {
		t.Errorf("InputTokens (uncached delta) want 12, got %v", evt.InputTokens)
	}
	if evt.CacheReadInputTokens == nil || *evt.CacheReadInputTokens != 4000 {
		t.Errorf("CacheReadInputTokens want 4000, got %v", evt.CacheReadInputTokens)
	}
	if evt.CacheCreationInputTokens == nil || *evt.CacheCreationInputTokens != 200 {
		t.Errorf("CacheCreationInputTokens want 200, got %v", evt.CacheCreationInputTokens)
	}
	if evt.TotalInputTokens == nil || *evt.TotalInputTokens != 4212 {
		t.Errorf("TotalInputTokens want 4212 (12+4000+200), got %v", evt.TotalInputTokens)
	}
	if evt.TokensUnavailable {
		t.Errorf("TokensUnavailable must be false — the call consumed cached input")
	}
	// Wire-name check: the emitted JSON must use the exact field names
	// security-next#36 reads.
	js := buf.String()
	for _, want := range []string{`"cache_read_input_tokens":4000`, `"cache_creation_input_tokens":200`, `"total_input_tokens":4212`} {
		if !strings.Contains(js, want) {
			t.Errorf("expected %s in JSON, got: %s", want, js)
		}
	}
}

func TestEmitLLMCall_NoCaching_TotalInputEqualsInputAndCacheFieldsOmitted(t *testing.T) {
	// Non-cached call (or non-Anthropic provider): total_input_tokens is
	// still emitted (so the security-next fallback stays on its fast
	// path) and equals input_tokens, while the two cache fields omit
	// cleanly to preserve the pre-#431 JSON shape.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "gpt-4o",
		Provider: "openai",
		Usage:    LLMUsage{InputTokens: 30, OutputTokens: 5, TotalTokens: 35},
		Duration: 1 * time.Millisecond,
	})
	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.TotalInputTokens == nil || *evt.TotalInputTokens != 30 {
		t.Errorf("TotalInputTokens want 30 (== input when no caching), got %v", evt.TotalInputTokens)
	}
	if evt.CacheReadInputTokens != nil || evt.CacheCreationInputTokens != nil {
		t.Errorf("cache fields must omit when zero, got read=%v creation=%v",
			evt.CacheReadInputTokens, evt.CacheCreationInputTokens)
	}
	js := buf.String()
	for _, forbidden := range []string{`"cache_read_input_tokens"`, `"cache_creation_input_tokens"`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("zero cache field %s must omit, got: %s", forbidden, js)
		}
	}
}

func TestEmitLLMCall_CacheReadOnly_NotFlaggedUnavailable(t *testing.T) {
	// A fully-cached turn reports input_tokens=0 but cache_read>0 — real
	// input was consumed, so tokens_unavailable must stay false (else
	// billing mistakes a large cached call for a free one).
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "claude-sonnet-4-6",
		Provider: "anthropic",
		Usage:    LLMUsage{InputTokens: 0, OutputTokens: 40, CacheReadInputTokens: 5000},
		Duration: 5 * time.Millisecond,
	})
	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.TokensUnavailable {
		t.Errorf("cache-read-only call consumed input; TokensUnavailable must be false, got %+v", evt)
	}
	if evt.TotalInputTokens == nil || *evt.TotalInputTokens != 5000 {
		t.Errorf("TotalInputTokens want 5000, got %v", evt.TotalInputTokens)
	}
}

func TestEmitLLMCall_TokensUnavailable_OllamaMissingUsage(t *testing.T) {
	// Self-hosted setups (some Ollama models) don't return token counts.
	// EmitLLMCall must flag tokens_unavailable=true rather than emit
	// silent zeros that downstream billing would mistake for a free call.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "llama3",
		Provider: "ollama",
		Usage:    LLMUsage{InputTokens: 0, OutputTokens: 0, TotalTokens: 0},
		Duration: 50 * time.Millisecond,
	})

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if !evt.TokensUnavailable {
		t.Errorf("TokensUnavailable should be true when both tokens are 0, got %+v", evt)
	}
	if evt.DurationMs == nil || *evt.DurationMs != 50 {
		t.Errorf("DurationMs must still be set, got %v", evt.DurationMs)
	}
}

func TestEmitLLMCall_Cancelled_EmitsLLMCallCancelledEvent(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:     "gpt-4",
		Provider:  "openai",
		Usage:     LLMUsage{InputTokens: 100, OutputTokens: 25, TotalTokens: 125},
		Duration:  200 * time.Millisecond,
		Cancelled: true,
	})

	js := buf.String()
	if !strings.Contains(js, `"event":"llm_call_cancelled"`) {
		t.Errorf("Cancelled should emit llm_call_cancelled, got: %s", js)
	}
	if !strings.Contains(js, `"input_tokens":100`) {
		t.Errorf("Cancelled event must still carry partial counts, got: %s", js)
	}
}

func TestEmitLLMCall_FieldNamesAlignWithOTelGenAI(t *testing.T) {
	// FWS-3 deliverable: field naming aligns with OTel GenAI semconv
	// (input_tokens / output_tokens matching gen_ai.usage.input_tokens
	// / gen_ai.usage.output_tokens). Audit consumers can correlate to
	// trace data without a translation table.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "claude",
		Provider: "anthropic",
		Usage:    LLMUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
		Duration: 1 * time.Millisecond,
	})
	js := buf.String()
	for _, want := range []string{`"input_tokens"`, `"output_tokens"`, `"duration_ms"`, `"model"`, `"provider"`} {
		if !strings.Contains(js, want) {
			t.Errorf("expected %s in JSON, got: %s", want, js)
		}
	}
	// Pre-OTel-rename names must NOT appear at the audit-event level
	// (legacy struct-name leakage).
	for _, forbidden := range []string{`"prompt_tokens":`, `"completion_tokens":`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("legacy field %s must not leak into llm_call audit, got: %s", forbidden, js)
		}
	}
}

func TestEmit_BackwardCompat_NonLLMEventOmitsTokenFields(t *testing.T) {
	// Schema additivity guarantee: events that aren't LLM calls must
	// emit without input_tokens / output_tokens / duration_ms / etc.
	// in the JSON. Pre-FWS-3 consumers reading session_start audit
	// must see byte-identical shape.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.Emit(AuditEvent{
		Event:         AuditSessionStart,
		CorrelationID: "corr-x",
		TaskID:        "task-x",
	})
	js := buf.String()
	for _, forbidden := range []string{`"input_tokens"`, `"output_tokens"`, `"duration_ms"`, `"model"`, `"provider"`, `"tokens_unavailable"`, `"request_id"`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("non-LLM event should omit %s, got: %s", forbidden, js)
		}
	}
}

func TestEmitToolExec_TagsDurationAndStructuredArgs(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitToolExec(context.Background(), "file_read", 12*time.Millisecond, map[string]any{
		"args_size":   42,
		"result_size": 1024,
	})

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.Event != AuditToolExec {
		t.Errorf("Event = %q, want %q", evt.Event, AuditToolExec)
	}
	if evt.DurationMs == nil || *evt.DurationMs != 12 {
		t.Errorf("DurationMs = %v, want 12", evt.DurationMs)
	}
	if evt.Fields["tool"] != "file_read" {
		t.Errorf("tool field missing")
	}
	if evt.Fields["args_size"] == nil {
		t.Errorf("args_size structured arg metadata missing — raw args must NOT be present, but size MUST")
	}
}

func TestEmitInvocationComplete_CarriesWallClockDuration(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitInvocationComplete(context.Background(), 950*time.Millisecond, map[string]any{
		"state":               "completed",
		"input_tokens_total":  200,
		"output_tokens_total": 80,
		"llm_call_count":      3,
	})

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.Event != AuditInvocationComplete {
		t.Errorf("Event = %q, want %q", evt.Event, AuditInvocationComplete)
	}
	if evt.DurationMs == nil || *evt.DurationMs != 950 {
		t.Errorf("DurationMs = %v, want 950", evt.DurationMs)
	}
	if v, ok := evt.Fields["llm_call_count"].(float64); !ok || v != 3 {
		t.Errorf("llm_call_count missing or wrong, got %v (%T)", evt.Fields["llm_call_count"], evt.Fields["llm_call_count"])
	}
}
