package runtime

import (
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// findGuardrailAttr returns the string value of a span attribute by
// key with ok=true only when the key was set. Mirrors the helper
// loop_spans_content_test.go uses for the LLM-span content tests.
func findGuardrailAttr(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

// findGuardrailIntAttr returns the int64 value of a span attribute by
// key (used for forge.guardrail.violation_count).
func findGuardrailIntAttr(span sdktrace.ReadOnlySpan, key string) (int64, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsInt64(), true
		}
	}
	return 0, false
}

// setupGuardrailTracingTest installs an in-memory tracer provider and
// returns an engine wired to it. The caller passes the TracingConfig
// to control CaptureContent / Redact independently.
func setupGuardrailTracingTest(t *testing.T, tracingCfg observability.TracingConfig) (*LibraryGuardrailEngine, *observability.SpanRecorder) {
	t.Helper()
	tp, rec := observability.NewTestTracerProvider()
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() {
		coreruntime.ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine: %v", err)
	}
	engine.WithTracing(tracingCfg)
	return engine, rec
}

// TestCheckInbound_OpensInputSpanWithGateAttributes verifies the
// guardrail.input span lands on the recorder with the gate, decision,
// and violation_count attributes stamped from the library Result.
// CaptureContent is OFF for this test — evidence MUST be absent.
func TestCheckInbound_OpensInputSpanWithGateAttributes(t *testing.T) {
	engine, rec := setupGuardrailTracingTest(t, observability.TracingConfig{})

	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "my email is foo@example.com"}},
	}
	if _, err := engine.CheckInbound(context.Background(), msg); err != nil {
		t.Fatalf("CheckInbound: %v", err)
	}

	span, ok := rec.FindSpan("guardrail.input")
	if !ok {
		t.Fatal("expected guardrail.input span")
	}

	if v, ok := findGuardrailAttr(span, observability.AttrForgeGuardrailGate); !ok || v != "input" {
		t.Errorf("gate = %q (ok=%v), want input", v, ok)
	}
	if v, ok := findGuardrailAttr(span, observability.AttrForgeGuardrailDecision); !ok || v != "masked" {
		t.Errorf("decision = %q (ok=%v), want masked", v, ok)
	}
	if vc, ok := findGuardrailIntAttr(span, observability.AttrForgeGuardrailViolationCount); !ok || vc < 1 {
		t.Errorf("violation_count = %d (ok=%v), want >= 1", vc, ok)
	}
	if _, ok := findGuardrailAttr(span, observability.AttrForgeGuardrailEvidence); ok {
		t.Errorf("evidence MUST be absent when CaptureContent=false")
	}
}

// TestCheckInbound_CaptureContent_StampsRedactedEvidence verifies the
// opt-in capture path: with CaptureContent=true the
// forge.guardrail.evidence attribute lands on the span carrying the
// post-mask content (same rule the audit-event evidence uses), and
// the raw PII MUST NOT appear in the attribute value.
func TestCheckInbound_CaptureContent_StampsRedactedEvidence(t *testing.T) {
	engine, rec := setupGuardrailTracingTest(t, observability.TracingConfig{CaptureContent: true, Redact: true})

	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "my email is foo@example.com"}},
	}
	if _, err := engine.CheckInbound(context.Background(), msg); err != nil {
		t.Fatalf("CheckInbound: %v", err)
	}

	span, ok := rec.FindSpan("guardrail.input")
	if !ok {
		t.Fatal("expected guardrail.input span")
	}

	ev, ok := findGuardrailAttr(span, observability.AttrForgeGuardrailEvidence)
	if !ok {
		t.Fatal("expected forge.guardrail.evidence attribute with CaptureContent=true")
	}
	if strings.Contains(ev, "foo@example.com") {
		t.Errorf("raw email MUST NOT appear in evidence (post-mask rule); got: %q", ev)
	}
}

// TestCheckToolCall_OpensToolCallSpanWithToolAttribute verifies the
// tool_call gate stamps forge.tool.name in addition to the standard
// guardrail attributes. SIEM consumers use the tool field to
// distinguish tool_call fires across many tools.
func TestCheckToolCall_OpensToolCallSpanWithToolAttribute(t *testing.T) {
	engine, rec := setupGuardrailTracingTest(t, observability.TracingConfig{})

	// Args that may or may not mask depending on rule config; we only
	// care here that the span lands with the tool attribute set.
	_, _ = engine.CheckToolCall(context.Background(), "send_email",
		`{"to":"alice@example.com","body":"hi"}`)

	span, ok := rec.FindSpan("guardrail.tool_call")
	if !ok {
		t.Fatal("expected guardrail.tool_call span")
	}
	if v, ok := findGuardrailAttr(span, observability.AttrForgeToolName); !ok || v != "send_email" {
		t.Errorf("forge.tool.name = %q (ok=%v), want send_email", v, ok)
	}
}

// TestCheckOutbound_OpensOutputSpan_NoToolAttribute verifies the
// "OutputGate on the model's reply to the user" path: span name is
// guardrail.output and forge.tool.name is absent (distinguishes it
// from CheckToolOutput which sets the tool attribute).
func TestCheckOutbound_OpensOutputSpan_NoToolAttribute(t *testing.T) {
	engine, rec := setupGuardrailTracingTest(t, observability.TracingConfig{})

	msg := &a2a.Message{
		Role:  "agent",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "Here is your answer."}},
	}
	if _, err := engine.CheckOutbound(context.Background(), msg); err != nil {
		t.Fatalf("CheckOutbound: %v", err)
	}

	span, ok := rec.FindSpan("guardrail.output")
	if !ok {
		t.Fatal("expected guardrail.output span")
	}
	if v, ok := findGuardrailAttr(span, observability.AttrForgeToolName); ok {
		t.Errorf("forge.tool.name should NOT be set on CheckOutbound (got %q)", v)
	}
}

// TestCheckContext_OpensContextSpan covers the ContextGate path.
func TestCheckContext_OpensContextSpan(t *testing.T) {
	engine, rec := setupGuardrailTracingTest(t, observability.TracingConfig{})

	if _, err := engine.CheckContext(context.Background(), "some retrieved context"); err != nil {
		t.Fatalf("CheckContext: %v", err)
	}

	if _, ok := rec.FindSpan("guardrail.context"); !ok {
		t.Fatal("expected guardrail.context span")
	}
}

// TestCheckStream_OpensStreamSpan covers the StreamGate path even
// though it's not auto-wired in the loop yet.
func TestCheckStream_OpensStreamSpan(t *testing.T) {
	engine, rec := setupGuardrailTracingTest(t, observability.TracingConfig{})

	if _, err := engine.CheckStream(context.Background(), "delta"); err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	if _, ok := rec.FindSpan("guardrail.stream"); !ok {
		t.Fatal("expected guardrail.stream span")
	}
}

// TestCheckInbound_NoTracing_NoSpansRecorded confirms the noop-tracer
// path: no SetTracerProvider, so Tracer() returns the noop tracer and
// no spans land on the recorder.
func TestCheckInbound_NoTracing_NoSpansRecorded(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	// Do NOT install the test provider — keep the noop default.
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine: %v", err)
	}
	// engine.WithTracing(observability.TracingConfig{}) is the default;
	// no tracer provider installed → noop tracer.

	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hello"}},
	}
	if _, err := engine.CheckInbound(context.Background(), msg); err != nil {
		t.Fatalf("CheckInbound: %v", err)
	}

	if _, ok := rec.FindSpan("guardrail.input"); ok {
		t.Errorf("expected no spans recorded with noop tracer; got guardrail.input")
	}
}
