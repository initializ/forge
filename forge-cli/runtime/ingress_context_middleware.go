package runtime

import (
	"net/http"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// installIngressContextMiddleware wraps the auth middleware so the
// per-invocation SequenceCounter AND the correlation ID are installed on the
// request context BEFORE the auth chain runs. The auth chain's OnAuth
// callback (which emits auth_verify / auth_fail) then sees both on its
// req.Context(): it stamps seq=1 on the first event and carries the same
// correlation_id (invocation id) the downstream task events will share. The
// runner's per-A2A-request setup calls EnsureSequenceCounter /
// EnsureCorrelationID, which detect the existing values and reuse them — so
// session_start lands at seq=2 with the SAME correlation_id, and the whole
// invocation (auth event included) is one gap-free, single-id timeline for
// FWS-8 / console-next run grouping.
//
// Minting the correlation id here (not at task creation) is the durable half
// of issue #278: pre-admission auth events could not be attributed to the
// invocation they guarded because the id didn't exist yet. Now it does, with
// true emission order preserved (auth precedes admission).
//
// Before the seq half (#174), the runner installed the counter at the
// JSON-RPC / REST handler entry, downstream of auth; the auth callback's
// audit emits used plain Emit() and lost seq + trace_id + workflow tags.
//
// Cost: ~24 bytes + a UUID per request. The wrapper runs even on auth-skipped
// paths (/.well-known/agent-card.json, /healthz); those don't emit
// per-request audit events, so the values are unused — allocating
// unconditionally is simpler than threading skip-path knowledge in.
func installIngressContextMiddleware(authMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Compose once: the auth middleware wraps next; we wrap THAT
		// composition so both values are installed before auth sees the
		// request.
		composed := authMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := coreruntime.EnsureSequenceCounter(r.Context())
			ctx = coreruntime.EnsureCorrelationID(ctx)
			composed.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
