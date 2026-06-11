package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/observability"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// awsKeyFixture is a deliberately obvious secret shape used across the
// capture-content tests so the scrub-vs-raw assertions stay readable.
const awsKeyFixture = "AKIAIOSFODNN7EXAMPLE"

// findAttr returns the string value of a span attribute by key, with
// `ok=true` only when the key was set on the span. Distinguishes
// "attribute absent" (the metadata-only signal) from "attribute set
// to the empty string" (which the executor should never produce).
func findAttr(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

// runOnePromptOneCompletion exercises the executor with a single-turn
// task (no tool calls) so the test focuses on the LLM-span content
// attributes. Returns the recorded llm.completion span.
func runOnePromptOneCompletion(t *testing.T, tracingCfg observability.TracingConfig, prompt, completion string) sdktrace.ReadOnlySpan {
	t.Helper()

	tp, rec := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: completion},
				Usage:        llm.UsageInfo{InputTokens: 50, OutputTokens: 10},
				FinishReason: "stop",
			}, nil
		},
	}

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         &mockToolExecutor{},
		MaxIterations: 3,
		ModelName:     "test-model",
		Provider:      "anthropic",
		TracingConfig: tracingCfg,
	})

	task := &a2a.Task{ID: "task-content"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: prompt}}}
	if _, err := exec.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	span, ok := rec.FindSpan("llm.completion")
	if !ok {
		t.Fatal("missing llm.completion span")
	}
	return span
}

// TestExecute_CaptureContentTrue_StampsRedactedPromptOnLLMSpan —
// issue #130 acceptance case. Operator opts in (CaptureContent=true,
// Redact=true), sends a prompt containing an AWS access key shape.
// The gen_ai.prompt attribute must be present and the raw key must
// NOT appear in its value.
func TestExecute_CaptureContentTrue_StampsRedactedPromptOnLLMSpan(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: true, Redact: true}
	span := runOnePromptOneCompletion(t, cfg,
		"deploy with key "+awsKeyFixture+" and reboot",
		"ok",
	)

	got, ok := findAttr(span, observability.AttrGenAIPrompt)
	if !ok {
		t.Fatal("gen_ai.prompt attribute missing — CaptureContent=true did not stamp")
	}
	if strings.Contains(got, awsKeyFixture) {
		t.Errorf("raw AWS key survived redaction on gen_ai.prompt:\n%s", got)
	}
	if !strings.Contains(got, RedactionMarker) {
		t.Errorf("expected redaction marker %q in gen_ai.prompt; got %q", RedactionMarker, got)
	}
}

// TestExecute_CaptureContentTrue_RedactFalse_StampsRawPromptOnLLMSpan
// is the enterprise raw-capture path — operator explicitly turned
// redaction off, accepting that span attributes will carry verbatim
// content. The cap is still applied but the secret is NOT scrubbed.
func TestExecute_CaptureContentTrue_RedactFalse_StampsRawPromptOnLLMSpan(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: true, Redact: false}
	span := runOnePromptOneCompletion(t, cfg,
		"key="+awsKeyFixture,
		"done",
	)

	got, ok := findAttr(span, observability.AttrGenAIPrompt)
	if !ok {
		t.Fatal("gen_ai.prompt missing")
	}
	if !strings.Contains(got, awsKeyFixture) {
		t.Errorf("Redact=false must preserve raw content; expected raw key in span attribute, got %q", got)
	}
	if strings.Contains(got, RedactionMarker) {
		t.Errorf("Redact=false must not insert redaction marker; got %q", got)
	}
}

// TestExecute_CaptureContentFalse_NoContentAttribute pins the default
// posture. With no opt-in, the gen_ai.prompt / gen_ai.completion
// attributes are absent — not set to empty string. Backends that
// look for "is the key present?" must see "no" so the
// metadata-only contract holds.
func TestExecute_CaptureContentFalse_NoContentAttribute(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: false}
	span := runOnePromptOneCompletion(t, cfg,
		"any prompt with "+awsKeyFixture,
		"any completion",
	)

	if _, ok := findAttr(span, observability.AttrGenAIPrompt); ok {
		t.Errorf("CaptureContent=false must not set gen_ai.prompt")
	}
	if _, ok := findAttr(span, observability.AttrGenAICompletion); ok {
		t.Errorf("CaptureContent=false must not set gen_ai.completion")
	}
}

// TestExecute_LargePrompt_TruncatesWithSameMarkerAsAudit verifies the
// cross-pipeline parity invariant — span content trimmed by the cap
// produces a marker byte-identical to what the audit
// payload-capture path produces for the same input.
func TestExecute_LargePrompt_TruncatesWithSameMarkerAsAudit(t *testing.T) {
	// 8 KiB of recognizable filler — twice the default span cap.
	bigPrompt := strings.Repeat("payload-", 1024)

	cfg := observability.TracingConfig{CaptureContent: true, Redact: false}
	span := runOnePromptOneCompletion(t, cfg, bigPrompt, "ok")

	got, ok := findAttr(span, observability.AttrGenAIPrompt)
	if !ok {
		t.Fatal("gen_ai.prompt missing")
	}

	// The executor serializes the messages into a JSON array, so the
	// audited input we compare against is the same JSON form. We use
	// the same helper the executor uses to produce that string, then
	// apply the same cap, and assert byte equality with the span
	// attribute's value.
	serialized := serializeChatMessages([]llm.ChatMessage{{Role: llm.RoleUser, Content: bigPrompt}})
	wantTruncated := TruncateForAudit(serialized, DefaultSpanContentCapBytes)

	if got != wantTruncated {
		t.Errorf("span content truncation diverged from audit truncation for the same input\n  span:  %q\n  audit: %q",
			got, wantTruncated)
	}
	if !strings.Contains(got, "[truncated:") {
		t.Errorf("expected truncation marker in gen_ai.prompt; got prefix %q…", got[:64])
	}
}

// TestExecute_CaptureContentTrue_StampsCompletionOnLLMSpan covers the
// happy completion path — when CaptureContent=true, the model's
// response text appears on the llm.completion span (post-success,
// before End).
func TestExecute_CaptureContentTrue_StampsCompletionOnLLMSpan(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: true, Redact: false}
	span := runOnePromptOneCompletion(t, cfg, "question", "the answer is 42")

	got, ok := findAttr(span, observability.AttrGenAICompletion)
	if !ok {
		t.Fatal("gen_ai.completion missing")
	}
	if got != "the answer is 42" {
		t.Errorf("gen_ai.completion = %q; want %q", got, "the answer is 42")
	}
}

// TestExecute_CaptureContentTrue_EmptyCompletion_SkipsAttribute
// matches the empty-content fast path in PrepareSpanContent and the
// `resp.Message.Content != ""` guard in loop.go — an assistant turn
// that returns no text (e.g. a tool-call-only response) should NOT
// stamp an empty completion attribute. Empty completion = no
// completion = no attribute.
func TestExecute_CaptureContentTrue_EmptyCompletion_SkipsAttribute(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: true, Redact: false}
	span := runOnePromptOneCompletion(t, cfg, "question", "")

	if _, ok := findAttr(span, observability.AttrGenAICompletion); ok {
		t.Errorf("empty completion must not stamp gen_ai.completion attribute")
	}
}

// runOneToolCall exercises the executor with one tool-call iteration
// so the test focuses on the tool.<name> span attributes. Returns the
// recorded tool span.
func runOneToolCall(t *testing.T, tracingCfg observability.TracingConfig, toolArgs, toolResult string) sdktrace.ReadOnlySpan {
	t.Helper()

	tp, rec := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	turn := 0
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			turn++
			if turn == 1 {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID:       "tc-1",
							Type:     "function",
							Function: llm.FunctionCall{Name: "echo", Arguments: toolArgs},
						}},
					},
					FinishReason: "tool_calls",
				}, nil
			}
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"},
				FinishReason: "stop",
			}, nil
		},
	}
	tools := &mockToolExecutor{
		executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return toolResult, nil
		},
	}

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         tools,
		MaxIterations: 3,
		ModelName:     "test-model",
		Provider:      "anthropic",
		TracingConfig: tracingCfg,
	})
	if _, err := exec.Execute(context.Background(),
		&a2a.Task{ID: "task-tool-content"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "go"}}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	span, ok := rec.FindSpan("tool.echo")
	if !ok {
		t.Fatal("missing tool.echo span")
	}
	return span
}

// TestExecute_CaptureContentTrue_StampsToolArgsAndResult exercises the
// tool-side mirror of the LLM-span content tests. Args from the
// LLM-emitted tool call land at forge.tool.args; tool stdout/return
// lands at forge.tool.result; both go through the same redact +
// truncate pipeline.
func TestExecute_CaptureContentTrue_StampsToolArgsAndResult(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: true, Redact: true}
	span := runOneToolCall(t, cfg, `{"target":"`+awsKeyFixture+`"}`, "deleted "+awsKeyFixture)

	args, ok := findAttr(span, observability.AttrForgeToolArgs)
	if !ok {
		t.Error("forge.tool.args missing when CaptureContent=true")
	} else {
		if strings.Contains(args, awsKeyFixture) {
			t.Errorf("raw AWS key survived redaction on forge.tool.args: %q", args)
		}
		if !strings.Contains(args, RedactionMarker) {
			t.Errorf("expected redaction marker in forge.tool.args; got %q", args)
		}
	}

	result, ok := findAttr(span, observability.AttrForgeToolResult)
	if !ok {
		t.Error("forge.tool.result missing when CaptureContent=true")
	} else {
		if strings.Contains(result, awsKeyFixture) {
			t.Errorf("raw AWS key survived redaction on forge.tool.result: %q", result)
		}
		if !strings.Contains(result, RedactionMarker) {
			t.Errorf("expected redaction marker in forge.tool.result; got %q", result)
		}
	}
}

// TestExecute_CaptureContentFalse_NoToolContentAttributes mirrors the
// default-posture check on the tool-call side.
func TestExecute_CaptureContentFalse_NoToolContentAttributes(t *testing.T) {
	cfg := observability.TracingConfig{CaptureContent: false}
	span := runOneToolCall(t, cfg, `{"target":"foo"}`, "ok")

	if _, ok := findAttr(span, observability.AttrForgeToolArgs); ok {
		t.Errorf("CaptureContent=false must not set forge.tool.args")
	}
	if _, ok := findAttr(span, observability.AttrForgeToolResult); ok {
		t.Errorf("CaptureContent=false must not set forge.tool.result")
	}
}
