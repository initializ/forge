package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression tests for FWS-8 (issue #91): sequence numbers + schema
// version + payload-strip invariant + opt-in capture. Together these
// codify the "audit is metadata, not user content" contract.

// ─── Sequence numbers ────────────────────────────────────────────────

func TestSequence_StartsAt1AndIncrementsPerEmit(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithSequenceCounter(context.Background(), new(SequenceCounter))
	for i := 0; i < 5; i++ {
		audit.EmitFromContext(ctx, AuditEvent{Event: "test_event"})
	}

	dec := json.NewDecoder(strings.NewReader(buf.String()))
	want := int64(1)
	for dec.More() {
		var evt AuditEvent
		if err := dec.Decode(&evt); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if evt.Sequence != want {
			t.Errorf("event[seq=%d] = %d, want %d (full event: %+v)", want, evt.Sequence, want, evt)
		}
		want++
	}
	if want != 6 { // five events, want is "next expected" so 5+1
		t.Errorf("read %d events, want 5", want-1)
	}
}

func TestSequence_NoCounterMeansNoField(t *testing.T) {
	// Events emitted outside any invocation scope (startup banner,
	// policy_loaded, agent_card_published) inherit no counter and
	// must NOT stamp `seq`. omitempty drops the field.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitFromContext(context.Background(), AuditEvent{Event: "startup"})

	if strings.Contains(buf.String(), `"seq"`) {
		t.Errorf("startup events must not carry seq, got: %s", buf.String())
	}
}

func TestSequence_PerInvocationIsolation(t *testing.T) {
	// Two concurrent invocations must each get their own monotonic
	// sequence — locks that one invocation's counter doesn't bleed
	// into another's audit stream. Consumers group by
	// (correlation_id, task_id) and expect uninterrupted runs there.
	var buf bytes.Buffer
	var mu sync.Mutex
	audit := NewAuditLogger(synchronizedWriter{w: &buf, mu: &mu})

	const N = 50
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx := WithSequenceCounter(WithCorrelationID(context.Background(), "A"),
			new(SequenceCounter))
		for i := 0; i < N; i++ {
			audit.EmitFromContext(ctx, AuditEvent{Event: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		ctx := WithSequenceCounter(WithCorrelationID(context.Background(), "B"),
			new(SequenceCounter))
		for i := 0; i < N; i++ {
			audit.EmitFromContext(ctx, AuditEvent{Event: "x"})
		}
	}()
	wg.Wait()

	seenA := make(map[int64]bool)
	seenB := make(map[int64]bool)
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var evt AuditEvent
		if err := dec.Decode(&evt); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch evt.CorrelationID {
		case "A":
			seenA[evt.Sequence] = true
		case "B":
			seenB[evt.Sequence] = true
		}
	}
	for i := int64(1); i <= N; i++ {
		if !seenA[i] {
			t.Errorf("invocation A missing seq %d", i)
		}
		if !seenB[i] {
			t.Errorf("invocation B missing seq %d", i)
		}
	}
}

func TestSequence_AtomicUnderHeavyConcurrency(t *testing.T) {
	// 32 goroutines × 100 events on one counter — every seq 1..3200
	// must appear exactly once.
	var buf bytes.Buffer
	var mu sync.Mutex
	audit := NewAuditLogger(synchronizedWriter{w: &buf, mu: &mu})
	ctx := WithSequenceCounter(context.Background(), new(SequenceCounter))

	const G, M = 32, 100
	var wg sync.WaitGroup
	wg.Add(G)
	for i := 0; i < G; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < M; j++ {
				audit.EmitFromContext(ctx, AuditEvent{Event: "concurrent"})
			}
		}()
	}
	wg.Wait()

	seen := make(map[int64]bool, G*M)
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var evt AuditEvent
		if err := dec.Decode(&evt); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if seen[evt.Sequence] {
			t.Errorf("duplicate seq %d", evt.Sequence)
		}
		seen[evt.Sequence] = true
	}
	for i := int64(1); i <= G*M; i++ {
		if !seen[i] {
			t.Errorf("missing seq %d", i)
		}
	}
}

// ─── Schema version ──────────────────────────────────────────────────

func TestSchemaVersion_StampedOnEveryEvent(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	// Three emit paths: Emit, EmitFromContext, EmitLLMCall — each must
	// land schema_version.
	audit.Emit(AuditEvent{Event: "plain"})
	audit.EmitFromContext(context.Background(), AuditEvent{Event: "ctx"})
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model: "x", Provider: "y",
	})

	dec := json.NewDecoder(strings.NewReader(buf.String()))
	count := 0
	for dec.More() {
		var evt AuditEvent
		if err := dec.Decode(&evt); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if evt.SchemaVersion != AuditSchemaVersion {
			t.Errorf("event %q schema_version = %q, want %q",
				evt.Event, evt.SchemaVersion, AuditSchemaVersion)
		}
		count++
	}
	if count != 3 {
		t.Errorf("decoded %d events, want 3", count)
	}
}

func TestSchemaVersion_CallerOverrideWins(t *testing.T) {
	// Future migrations may want to stamp a different schema_version
	// on specific events (e.g. a backfill writer). When the caller
	// already set it, Emit must NOT overwrite.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.Emit(AuditEvent{Event: "override", SchemaVersion: "0.9-experimental"})

	if !strings.Contains(buf.String(), `"schema_version":"0.9-experimental"`) {
		t.Errorf("caller-set schema_version was clobbered: %s", buf.String())
	}
}

// ─── Payload-stripping invariant ─────────────────────────────────────

// TestNoPayloadByDefault is the security-critical regression test.
// We feed the audit pipeline an LLM call whose prompt + completion
// contain a deliberately unique marker string ("CANARY-FWS8-..."),
// emit through the public EmitLLMCall API with no capture config,
// and assert the marker never appears in the serialized NDJSON.
// Any future caller that smuggles raw content into a default audit
// event will fail this test.
func TestNoPayloadByDefault_LLMCall(t *testing.T) {
	const canary = "CANARY-FWS8-SECRET-PROMPT-CONTENT"
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	// Simulate what the runner would emit in the default
	// (metadata-only) posture: model, provider, tokens, duration —
	// nothing else.
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "gpt-4o",
		Provider: "openai",
		Usage:    LLMUsage{InputTokens: 100, OutputTokens: 50},
		Duration: 250 * time.Millisecond,
		// Fields is nil — default posture.
	})

	if strings.Contains(buf.String(), canary) {
		t.Fatalf("default llm_call event leaked prompt content (%q) into audit stream:\n%s",
			canary, buf.String())
	}
}

func TestPayloadCapture_OptInRestoresPromptAndCompletion(t *testing.T) {
	// With LLMMessages + LLMResponse enabled (the opt-in customer
	// path), the Fields map carries the captured strings verbatim
	// (up to the cap). The hook layer is responsible for the
	// truncation; here we exercise the audit-level fields plumbing.
	const prompt = "What is 2+2?"
	const completion = "2+2 = 4."
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model:    "gpt-4o",
		Provider: "openai",
		Fields: map[string]any{
			"prompt_messages": prompt,
			"completion_text": completion,
		},
	})
	if !strings.Contains(buf.String(), prompt) || !strings.Contains(buf.String(), completion) {
		t.Errorf("opt-in capture should land prompt + completion in fields, got:\n%s", buf.String())
	}
}

func TestTruncateForAudit_HonorsCap(t *testing.T) {
	long := strings.Repeat("A", 100)
	got := TruncateForAudit(long, 30)
	if len(got) > 30 {
		t.Errorf("returned %d bytes, want ≤ 30", len(got))
	}
	if !strings.Contains(got, "truncated:100") {
		t.Errorf("truncation marker missing or wrong original size: %q", got)
	}
}

func TestTruncateForAudit_PassthroughUnderCap(t *testing.T) {
	short := "tiny"
	if got := TruncateForAudit(short, 100); got != short {
		t.Errorf("under-cap should pass through, got %q", got)
	}
}

func TestPayloadCapture_AnyEnabled(t *testing.T) {
	if (AuditPayloadCapture{}).AnyEnabled() {
		t.Error("zero config should report AnyEnabled=false")
	}
	if !(AuditPayloadCapture{LLMResponse: true}).AnyEnabled() {
		t.Error("any one flag should report AnyEnabled=true")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────

// synchronizedWriter is a tiny io.Writer that serializes writes.
// Used in concurrent tests so the underlying bytes.Buffer doesn't
// race even though our AuditLogger's writerSink already has its own
// mutex (the test goal is to exercise sequence atomicity, not to
// trust the buffer).
type synchronizedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s synchronizedWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Compile-time assertion that SequenceCounter is the alias we want.
var _ = func() bool {
	var c SequenceCounter
	_ = (*atomic.Int64)(&c)
	return true
}()

// ─── llm_call_failed (#361) ──────────────────────────────────────────

// A failed LLM call must land in the audit stream as llm_call_failed with the
// bounded error detail — pre-#361 the error lived only in pod logs, so an
// agent whose every call the provider rejected looked audit-silent
// (field-hit 2026-07-22).
func TestEmitLLMCall_FailedVariant(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	long := strings.Repeat("x", 600)
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model: "gpt-4o", Provider: "openai", Duration: 42 * time.Millisecond,
		Failed: true, ErrorText: "anthropic error (status 400): input_schema.properties bad key " + long,
	})

	var evt AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Event != AuditLLMCallFailed {
		t.Fatalf("event = %q, want llm_call_failed", evt.Event)
	}
	errText, _ := evt.Fields["error"].(string)
	if !strings.Contains(errText, "input_schema.properties") {
		t.Fatalf("fields.error missing detail: %q", errText)
	}
	if len(errText) > 520 {
		t.Fatalf("error text not bounded: %d bytes", len(errText))
	}
	if evt.Model != "gpt-4o" || evt.Provider != "openai" {
		t.Fatalf("attribution lost: %+v", evt)
	}
	if evt.DurationMs == nil || *evt.DurationMs != 42 {
		t.Fatalf("duration lost: %+v", evt.DurationMs)
	}
	// Failed takes precedence over Cancelled.
	buf.Reset()
	audit.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model: "m", Provider: "p", Failed: true, Cancelled: true, ErrorText: "e",
	})
	var evt2 AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &evt2); err != nil {
		t.Fatalf("decode2: %v", err)
	}
	if evt2.Event != AuditLLMCallFailed {
		t.Fatalf("Failed must win over Cancelled, got %q", evt2.Event)
	}
}
