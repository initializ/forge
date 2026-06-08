package runtime

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// InstrumentationName is the OTel instrumentation scope name used for
// every tracer Forge obtains. Pinned to the module path so OTel
// backends can distinguish Forge spans from spans emitted by other
// instrumentation in the same process. Subsequent phases (#102–#107
// in the OTel v1 initiative, #108) read this same constant — do not
// duplicate it.
const InstrumentationName = "github.com/initializ/forge"

// tracerProvider holds the process-wide OTel TracerProvider. Defaults
// to the OTel no-op provider so calling Tracer() before anything is
// configured is safe and zero-cost — a non-recording Span comes back,
// downstream code paths cost the few nanoseconds of an interface
// dispatch and nothing else.
//
// Real OTLP providers live in the forge-core/observability subpackage
// (Phase 1, #102) and are injected from the cli-level wiring (Phase 2,
// #103) via SetTracerProvider. forge-core itself never constructs a
// real provider — that keeps the OTLP exporter dependencies off the
// dependency closure of test runs that don't need them and preserves
// the "tracing off by default, audit unchanged" invariant the
// initiative ruled on.
var (
	tracerProviderMu sync.RWMutex
	tracerProvider   trace.TracerProvider = noop.NewTracerProvider()
)

// SetTracerProvider installs the given TracerProvider as the
// process-wide tracer source for forge-core. Also installs it as the
// OTel global so any third-party library Forge depends on (the OTLP
// exporter's own transport, future runtime-loaded SDKs) sees the same
// provider when it calls otel.Tracer() directly. Also installs the
// W3C tracecontext + baggage composite propagator as the OTel global
// text-map propagator so inbound/outbound HTTP plumbing (Phase 5,
// #106) can extract / inject trace context without further setup.
//
// Calling SetTracerProvider with nil is a no-op (defensive guard for
// the cli wiring path — a misconfigured exporter resolution must not
// install a nil provider that would crash on the first Tracer call).
//
// Safe to call from any goroutine. Subsequent calls replace the
// previous provider; intended to fire exactly once at agent startup.
func SetTracerProvider(tp trace.TracerProvider) {
	if tp == nil {
		return
	}
	tracerProviderMu.Lock()
	tracerProvider = tp
	tracerProviderMu.Unlock()

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// ResetTracerProviderForTest restores the no-op provider. Exists so
// tests that install a real provider can cleanly tear down. Not
// part of the production wiring — kept exported so tests in the
// forge-core/observability subpackage (Phase 1) can also use it.
func ResetTracerProviderForTest() {
	tracerProviderMu.Lock()
	tracerProvider = noop.NewTracerProvider()
	tracerProviderMu.Unlock()

	otel.SetTracerProvider(noop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// Tracer returns a tracer scoped to the Forge instrumentation name.
// When no provider has been installed, the returned tracer is the
// no-op tracer — every span it produces has IsValid() == false and
// records nothing. Hot-path code calls Tracer().Start unconditionally;
// the no-op short-circuit is cheap by design.
func Tracer() trace.Tracer {
	tracerProviderMu.RLock()
	tp := tracerProvider
	tracerProviderMu.RUnlock()
	return tp.Tracer(InstrumentationName)
}
