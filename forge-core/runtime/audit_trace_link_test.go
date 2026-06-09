package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/observability"
)

// TestEmitFromContext_StampsTraceIDsFromRecordingSpan pins the Phase 4
// (#105) cross-link invariant: when the context carries a recording
// span, the resulting audit event carries that span's trace_id and
// span_id as lowercase-hex W3C-style strings. Operators paste these
// directly into their backend (Tempo, Jaeger, Honeycomb) to pivot
// from an audit row to the parent trace.
func TestEmitFromContext_StampsTraceIDsFromRecordingSpan(t *testing.T) {
	tp, _ := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	ctx, span := Tracer().Start(context.Background(), "test-span")
	defer span.End()

	al.EmitFromContext(ctx, AuditEvent{Event: AuditToolExec})

	var got AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("decode: %v\nline: %s", err, buf.String())
	}

	wantTrace := span.SpanContext().TraceID().String()
	wantSpan := span.SpanContext().SpanID().String()
	if got.TraceID != wantTrace {
		t.Errorf("trace_id = %q, want %q", got.TraceID, wantTrace)
	}
	if got.SpanID != wantSpan {
		t.Errorf("span_id = %q, want %q", got.SpanID, wantSpan)
	}
	// Format conformance: lowercase hex, 32 chars / 16 chars per W3C
	// traceparent. Pin both so a future change to a non-standard
	// encoding (uppercase, dashed) is caught at CI.
	if len(got.TraceID) != 32 || strings.ToLower(got.TraceID) != got.TraceID {
		t.Errorf("trace_id must be 32-char lowercase hex; got %q", got.TraceID)
	}
	if len(got.SpanID) != 16 || strings.ToLower(got.SpanID) != got.SpanID {
		t.Errorf("span_id must be 16-char lowercase hex; got %q", got.SpanID)
	}
}

// TestEmitFromContext_OmitsTraceIDsWhenNoSpan confirms the backward-
// compatibility invariant: an emit from a bare context.Background()
// (no span on the context, tracer is the package-default noop)
// produces JSON with NEITHER trace_id NOR span_id keys present.
// Audit-event consumers that haven't been upgraded continue to see
// the pre-Phase-4 shape byte-identically.
func TestEmitFromContext_OmitsTraceIDsWhenNoSpan(t *testing.T) {
	// Force the default noop tracer so the test doesn't inherit a
	// recording provider from a sibling test in the same package.
	ResetTracerProviderForTest()

	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	al.EmitFromContext(context.Background(), AuditEvent{Event: AuditToolExec})

	line := strings.TrimSpace(buf.String())
	// JSON-level absence check — the keys must NOT appear in the
	// serialized output (omitempty must drop them when zero-valued).
	if strings.Contains(line, `"trace_id"`) {
		t.Errorf("trace_id must be omitted when no span; got line: %s", line)
	}
	if strings.Contains(line, `"span_id"`) {
		t.Errorf("span_id must be omitted when no span; got line: %s", line)
	}
}

// TestEmitFromContext_NoopSpanProducesNoTraceIDs targets the specific
// failure mode where tracing is "off" (noop provider installed) but
// some code still calls Tracer().Start(...). The noop span's
// SpanContext().IsValid() must be false → trace_id / span_id must be
// omitted. Without this guard a Phase-2-disabled deploy would still
// emit noisy invalid-id fields.
func TestEmitFromContext_NoopSpanProducesNoTraceIDs(t *testing.T) {
	ResetTracerProviderForTest()

	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	ctx, span := Tracer().Start(context.Background(), "noop-span")
	defer span.End()

	al.EmitFromContext(ctx, AuditEvent{Event: AuditToolExec})

	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, `"trace_id"`) || strings.Contains(line, `"span_id"`) {
		t.Errorf("noop span must NOT produce trace_id/span_id (tracing-off invariant); got line: %s", line)
	}
}

// TestEmitFromContext_ExplicitTraceIDsWin protects the "context is a
// fallback, not an override" invariant the rest of EmitFromContext
// already honors for CorrelationID / TaskID / WorkflowID. A caller
// that pre-stamps TraceID/SpanID on the AuditEvent gets exactly those
// values out — not the span's.
func TestEmitFromContext_ExplicitTraceIDsWin(t *testing.T) {
	tp, _ := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	ctx, span := Tracer().Start(context.Background(), "ambient")
	defer span.End()

	const explicitTrace = "deadbeefcafebabe1234567890abcdef"
	const explicitSpan = "facefacefaceface"
	al.EmitFromContext(ctx, AuditEvent{
		Event:   AuditToolExec,
		TraceID: explicitTrace,
		SpanID:  explicitSpan,
	})

	var got AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got.TraceID != explicitTrace {
		t.Errorf("explicit trace_id was overwritten (got %q want %q)", got.TraceID, explicitTrace)
	}
	if got.SpanID != explicitSpan {
		t.Errorf("explicit span_id was overwritten (got %q want %q)", got.SpanID, explicitSpan)
	}
}

// TestEmitFromContext_TraceLinkMatchesEmittedSpanTree is the end-to-end
// join check: an executor-style flow (parent span → child llm.completion
// → audit emit) produces audit events whose span_id matches whichever
// span was active at emit time. Pivots from a span's
// gen_ai.usage.input_tokens attribute to the corresponding llm_call
// audit row depend on this — they're the same logical event captured
// in two pipelines.
func TestEmitFromContext_TraceLinkMatchesEmittedSpanTree(t *testing.T) {
	tp, _ := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	var buf bytes.Buffer
	al := NewAuditLogger(&buf)

	ctx, parent := Tracer().Start(context.Background(), "agent.execute")
	defer parent.End()

	childCtx, child := Tracer().Start(ctx, "llm.completion")

	// Emit from the CHILD context — the linked span_id must be the
	// child's, not the parent's. Otherwise correlated dashboards would
	// always pivot to the parent and lose the inner step.
	al.EmitFromContext(childCtx, AuditEvent{Event: "llm_call"})
	child.End()

	// Then emit from the PARENT context after child ended — the
	// linked span_id must be the parent's.
	al.EmitFromContext(ctx, AuditEvent{Event: "invocation_complete"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 audit lines, got %d:\n%s", len(lines), buf.String())
	}

	var llmCall, invComplete AuditEvent
	_ = json.Unmarshal([]byte(lines[0]), &llmCall)
	_ = json.Unmarshal([]byte(lines[1]), &invComplete)

	parentTrace := parent.SpanContext().TraceID().String()
	parentSpan := parent.SpanContext().SpanID().String()
	childSpan := child.SpanContext().SpanID().String()

	if llmCall.TraceID != parentTrace || invComplete.TraceID != parentTrace {
		t.Errorf("both events must share parent trace_id; got llm=%q inv=%q want=%q",
			llmCall.TraceID, invComplete.TraceID, parentTrace)
	}
	if llmCall.SpanID != childSpan {
		t.Errorf("llm_call must link to child span; got %q want %q", llmCall.SpanID, childSpan)
	}
	if invComplete.SpanID != parentSpan {
		t.Errorf("invocation_complete must link to parent span; got %q want %q", invComplete.SpanID, parentSpan)
	}
}
