package server

import (
	"context"
	"net/http"
)

// ResponseHeaderStage is the side-channel JSON-RPC handlers use to
// publish per-invocation response headers (FWS-3's X-Forge-Tokens-In,
// X-Forge-Duration-Ms, X-Forge-Model, ...). The stage exists because
// Handler's signature deliberately omits http.ResponseWriter — JSON-RPC
// dispatch is generic and the dispatcher writes the envelope, not the
// handler. Headers that depend on handler-computed state (token totals
// captured inside executeTask, the resolved primary model / provider,
// the wall-clock invocation duration) need a place to live between
// "handler computed the values" and "dispatcher writes the response."
//
// Lifecycle, owned by handleJSONRPC:
//
//	1. Dispatcher attaches an empty http.Header to ctx via
//	   WithResponseHeaderStage.
//	2. Handler runs; if it has invocation-usage data it reads the stage
//	   via ResponseHeaderStageFromContext and stamps headers onto it.
//	3. Dispatcher merges the stage onto the real ResponseWriter's
//	   Header() before writeJSON emits the body.
//
// REST handlers don't need the stage — they hold the writer directly
// and stamp via the existing applyForgeUsageHeaders path. The stage
// is the JSON-RPC equivalent.
//
// nil-stage reads from handlers must be a no-op so non-JSON-RPC code
// paths (REST, programmatic tests) don't crash on a missing stage.
// ResponseHeaderStageFromContext returns nil in that case; callers
// guard with `if stage != nil`.
//
// See PR fixing JSON-RPC vs REST X-Forge-* header parity.

type responseHeaderStageKey struct{}

// WithResponseHeaderStage attaches a fresh, empty http.Header to ctx.
// Call this from the JSON-RPC dispatcher exactly once per request,
// before invoking the registered Handler. The returned context must
// be the one passed to the Handler; otherwise the handler's
// ResponseHeaderStageFromContext lookup will miss.
func WithResponseHeaderStage(ctx context.Context) context.Context {
	return context.WithValue(ctx, responseHeaderStageKey{}, http.Header{})
}

// ResponseHeaderStageFromContext returns the staging header attached
// by WithResponseHeaderStage, or nil when no stage was installed.
// Handlers that want to publish per-invocation response headers
// (typed FWS-3 usage telemetry, future cancellation reasons, etc.)
// read this and write to it. The dispatcher drains it via
// DrainResponseHeaderStage after the handler returns.
func ResponseHeaderStageFromContext(ctx context.Context) http.Header {
	if h, ok := ctx.Value(responseHeaderStageKey{}).(http.Header); ok {
		return h
	}
	return nil
}

// DrainResponseHeaderStage copies every staged header from ctx onto
// the destination header (typically the response writer's). Uses Add
// (not Set) so handlers that legitimately want multi-value headers
// can produce them, though FWS-3's surface uses single-valued
// headers only.
//
// nil stage → no-op. Empty stage → no-op. Called by the dispatcher
// between Handler return and writeJSON so the headers land on the
// envelope.
func DrainResponseHeaderStage(ctx context.Context, dst http.Header) {
	stage := ResponseHeaderStageFromContext(ctx)
	if len(stage) == 0 {
		return
	}
	for k, vs := range stage {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
