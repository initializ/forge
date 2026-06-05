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
