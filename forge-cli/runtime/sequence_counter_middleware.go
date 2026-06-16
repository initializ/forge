package runtime

import (
	"net/http"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// installSequenceCounterMiddleware wraps the auth middleware so the
// per-invocation SequenceCounter is installed on the request context
// BEFORE the auth chain runs. The auth chain's OnAuth callback (which
// emits auth_verify / auth_fail) then sees a counter on its
// req.Context() and stamps seq=1 on the first event. The runner's
// per-A2A-request setup further downstream calls
// coreruntime.EnsureSequenceCounter, which detects the existing
// counter and reuses it — so session_start lands at seq=2, llm_call
// at seq=3, and the per-correlation_id sequence is gap-free for
// FWS-8 consumers.
//
// Before this wrapper, the runner's setup installed the counter at
// the JSON-RPC / REST handler entry, which is downstream of auth.
// The auth callback's audit emits had to use plain Emit() and lost
// seq + trace_id + workflow-correlation tags. See issue #174.
//
// Cost: ~24 bytes per request for the SequenceCounter allocation.
// The wrapper runs even on auth-skipped paths
// (/.well-known/agent-card.json, /healthz). Those paths don't emit
// per-request audit events, so the counter is unused — but allocating
// unconditionally is simpler than threading skip-path knowledge into
// the wrapper, and the allocation is in the same ballpark as the
// request struct itself.
func installSequenceCounterMiddleware(authMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Compose once: the auth middleware wraps next; we wrap THAT
		// composition so the seq counter is installed before auth sees
		// the request.
		composed := authMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := coreruntime.EnsureSequenceCounter(r.Context())
			composed.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
