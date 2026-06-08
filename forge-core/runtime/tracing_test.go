package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestTracing_DefaultIsNoOp pins the Phase 0 invariant: with no
// SetTracerProvider call, Tracer() returns a tracer that produces
// non-recording spans. Every subsequent OTel phase relies on this —
// if the default ever flips to a recording provider, the audit-by-
// default invariant becomes "audit AND traces by default" which is
// the wrong posture per the initiative ruling (tracing off by default).
func TestTracing_DefaultIsNoOp(t *testing.T) {
	// Force a clean state — earlier tests in this package may have
	// installed a provider via SetTracerProvider.
	ResetTracerProviderForTest()

	ctx, span := Tracer().Start(context.Background(), "test-span")
	defer span.End()

	if span.SpanContext().IsValid() {
		t.Errorf("default Tracer().Start span context must be invalid (no-op); got %+v",
			span.SpanContext())
	}
	if span.SpanContext().HasTraceID() {
		t.Error("no-op span must not carry a trace id")
	}
	if span.IsRecording() {
		t.Error("no-op span must not record")
	}
	// The returned ctx still contains the span (otel's noop puts it
	// there) but the span is non-recording — exactly the cheap-on-
	// the-hot-path shape Phase 3's instrumentation depends on.
	if got := trace.SpanFromContext(ctx); got.SpanContext().IsValid() {
		t.Error("context-derived span must also be invalid under no-op")
	}
}

// TestTracing_SetTracerProvider_SwapsLocalAndGlobal confirms that
// installing a provider via SetTracerProvider:
//  1. Replaces the local package-level provider so Tracer() sees it,
//  2. Installs the same provider as the OTel global so any third-
//     party library that calls otel.Tracer directly sees it too.
func TestTracing_SetTracerProvider_SwapsLocalAndGlobal(t *testing.T) {
	defer ResetTracerProviderForTest()

	// Use a recording stub provider so we can assert spans are real.
	tp := &recordingTracerProvider{}
	SetTracerProvider(tp)

	// Invoke the package seam — must reach the installed provider.
	_ = Tracer()
	if !tp.requested(InstrumentationName) {
		t.Errorf("Tracer() (via package seam) must request the Forge instrumentation scope; got %v", tp.requestedNames())
	}

	// Reset the count and confirm otel.Tracer() also reaches our provider.
	tp.reset()
	_ = otel.Tracer("third-party-instrumentation")
	if !tp.requested("third-party-instrumentation") {
		t.Error("otel global Tracer() must reach the installed provider after SetTracerProvider")
	}
}

// TestTracing_SetTracerProvider_NilIsNoOp guards against a
// misconfigured exporter resolution path installing a nil provider
// (which would crash the first Tracer() call). Defensive — should
// stay the noop.
func TestTracing_SetTracerProvider_NilIsNoOp(t *testing.T) {
	defer ResetTracerProviderForTest()

	// Install a known provider, then try to "set" nil — must not clobber.
	known := &recordingTracerProvider{}
	SetTracerProvider(known)
	SetTracerProvider(nil)

	// The known provider should still be the one Tracer() reaches.
	known.reset()
	_ = Tracer()
	if !known.requested(InstrumentationName) {
		t.Error("nil SetTracerProvider must NOT clobber the previously installed provider")
	}
}

// TestTracing_PropagatorRoundTrip confirms SetTracerProvider also
// installs the composite traceparent + baggage propagator on the OTel
// global. Phase 5 (#106) relies on this for HTTP propagation: the
// inbound dispatcher extracts via otel.GetTextMapPropagator(), the
// outbound HTTP plumbing injects via the same global.
func TestTracing_PropagatorRoundTrip(t *testing.T) {
	defer ResetTracerProviderForTest()
	SetTracerProvider(&recordingTracerProvider{})

	prop := otel.GetTextMapPropagator()
	if prop == nil {
		t.Fatal("composite propagator must be installed by SetTracerProvider")
	}

	// We don't have a real recording span to inject (the stub is
	// inert), so test the round-trip via inject + extract of a
	// synthetic traceparent header. The contract: a TraceContext-
	// compliant header on the inbound request comes out of Extract as
	// a valid SpanContext.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	ctx := prop.Extract(req.Context(), propagation.HeaderCarrier(req.Header))
	got := trace.SpanContextFromContext(ctx)
	if !got.IsValid() {
		t.Fatalf("propagator must extract the well-formed traceparent header into a valid SpanContext; got %+v", got)
	}
	if got.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("extracted trace_id = %q, want 0af7651916cd43dd8448eb211c80319c", got.TraceID())
	}
	if got.SpanID().String() != "b7ad6b7169203331" {
		t.Errorf("extracted span_id = %q, want b7ad6b7169203331", got.SpanID())
	}
}

// TestTracing_InstrumentationNameIsModulePath protects the constant
// every OTel phase reads. Backends route Forge spans by this scope
// name; flipping it breaks downstream dashboards silently.
func TestTracing_InstrumentationNameIsModulePath(t *testing.T) {
	if InstrumentationName != "github.com/initializ/forge" {
		t.Errorf("InstrumentationName = %q, want %q (the module path)",
			InstrumentationName, "github.com/initializ/forge")
	}
}

// ─── stub provider for tests ─────────────────────────────────────────

// recordingTracerProvider is a minimal trace.TracerProvider stub that
// records every instrumentation scope name a caller asks for. Real
// providers (SDK with OTLP exporter) land in Phase 1; this stub is
// here just so Phase 0 can assert the swap behavior without pulling
// the SDK into the test binary.
type recordingTracerProvider struct {
	noop.TracerProvider
	requested_ []string
}

func (r *recordingTracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	r.requested_ = append(r.requested_, name)
	return r.TracerProvider.Tracer(name, opts...)
}

func (r *recordingTracerProvider) requested(name string) bool {
	for _, n := range r.requested_ {
		if n == name {
			return true
		}
	}
	return false
}

func (r *recordingTracerProvider) requestedNames() []string { return r.requested_ }
func (r *recordingTracerProvider) reset()                   { r.requested_ = nil }
