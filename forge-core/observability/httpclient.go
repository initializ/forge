package observability

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WrapHTTPTransport decorates an http.RoundTripper with OpenTelemetry
// HTTP instrumentation. Every request the wrapped transport handles
// produces an "http.client" span automatically — method, host,
// status code, and trace-context injection come for free, with no
// per-call-site changes.
//
// Wiring is one line at the runner setup point: after the egress
// enforcer builds its transport, the runner passes it through this
// wrapper before stashing the http.Client downstream code uses. LLM
// providers / MCP clients / channel adapters that retrieve the
// egress-enforced client therefore get HTTP-level spans for free.
//
// Nil-tolerant: passing nil returns nil so a caller that builds a
// transport conditionally (and decided not to) doesn't have to guard.
//
// Span propagation: otelhttp.NewTransport reads the OTel global
// TextMapPropagator (Phase 0 installs traceparent + baggage). The
// outbound request therefore carries the parent span's
// traceparent header automatically — Phase 5 (#106) end-to-end
// propagation rides on this without further work here.
//
// The instrumentation honors the global TracerProvider, so when the
// tracer is the noop (tracing disabled) the wrapper is effectively a
// pass-through — no per-request overhead beyond a single interface
// dispatch.
func WrapHTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		return nil
	}
	return otelhttp.NewTransport(base)
}
