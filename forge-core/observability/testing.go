package observability

import (
	"context"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanRecorder is an in-memory sdktrace.SpanExporter for tests. It
// captures every finished span and returns the captured slice on
// demand so test assertions can pin span hierarchy, attributes, and
// status without spinning up a real collector.
//
// Production code must NEVER use this — the recorder retains every
// span forever. Phase 3 instrumentation tests (#104) and any future
// phase that asserts on spans use it via NewTestTracerProvider.
type SpanRecorder struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

// ExportSpans implements sdktrace.SpanExporter.
func (r *SpanRecorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	r.spans = append(r.spans, spans...)
	r.mu.Unlock()
	return nil
}

// Shutdown implements sdktrace.SpanExporter. The recorder has no
// background goroutines so Shutdown is a no-op; the test owns the
// recorder's lifecycle.
func (r *SpanRecorder) Shutdown(_ context.Context) error { return nil }

// Spans returns a snapshot of every span recorded so far. Safe to call
// from any goroutine; the returned slice does not share backing memory
// with the recorder so a test may sort / mutate it freely.
func (r *SpanRecorder) Spans() []sdktrace.ReadOnlySpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdktrace.ReadOnlySpan, len(r.spans))
	copy(out, r.spans)
	return out
}

// FindSpan returns the first recorded span whose name matches. Useful
// when a test produces one span per name (the common case for
// non-loop instrumentation). For loop sites (per-iteration spans of
// the same name), iterate Spans() directly.
func (r *SpanRecorder) FindSpan(name string) (sdktrace.ReadOnlySpan, bool) {
	for _, s := range r.Spans() {
		if s.Name() == name {
			return s, true
		}
	}
	return nil, false
}

// FindSpans returns every recorded span whose name matches, in the
// order spans were exported.
func (r *SpanRecorder) FindSpans(name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range r.Spans() {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

// Reset clears every recorded span. Call between test sub-stages when
// reusing the same recorder.
func (r *SpanRecorder) Reset() {
	r.mu.Lock()
	r.spans = nil
	r.mu.Unlock()
}

// NewTestTracerProvider constructs an sdktrace.TracerProvider that
// records every span synchronously into the returned SpanRecorder.
// The processor is *SimpleSpanProcessor* (not BatchSpanProcessor) so
// tests do not need to call ForceFlush before reading recorded spans —
// every span is exported on End.
//
// Sampler is AlwaysSample so a test can install the provider and
// immediately get spans regardless of the production sampler default.
//
// Typical test setup:
//
//	tp, rec := observability.NewTestTracerProvider()
//	coreruntime.SetTracerProvider(tp)
//	defer coreruntime.ResetTracerProviderForTest()
//	defer tp.Shutdown(context.Background())
//	// ... exercise code under test ...
//	got := rec.FindSpans("agent.execute")
func NewTestTracerProvider() (*sdktrace.TracerProvider, *SpanRecorder) {
	rec := &SpanRecorder{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(rec),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	return tp, rec
}
