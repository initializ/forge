package server

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// admissionResponseBody is the JSON envelope Forge writes on the 402
// Payment Required response when the admission middleware denies an
// inbound invocation. The shape mirrors the platform's admission
// response so a caller library can deserialize them with one struct.
type admissionResponseBody struct {
	Error   string `json:"error"`
	Reason  string `json:"reason,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Window  string `json:"window,omitempty"`
	ResetAt string `json:"reset_at,omitempty"`
}

// AdmissionMiddleware gates inbound A2A requests on the platform's
// per-agent quota decision (issue #201). Wraps `next` with a check
// that runs AFTER the auth middleware on every request — denied
// requests short-circuit with HTTP 402 Payment Required + a
// structured body + the `task_admission_denied` audit event.
//
// Forge fails open on platform errors (see PlatformAdmissionChecker
// fallback path). On admit the middleware is a pass-through; on
// deny it never reaches `next`.
//
// Audit logging is optional — passing nil disables emission. The
// span is opened inside the checker (not here) so the underlying
// http.client call to the platform nests cleanly under
// admission.check; both wrapped and unwrapped paths benefit.
func AdmissionMiddleware(
	checker coreruntime.AdmissionChecker,
	auditLogger *coreruntime.AuditLogger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if checker == nil {
			return next
		}
		if _, noop := checker.(coreruntime.NoopAdmissionChecker); noop {
			// Skip the wrapping cost entirely on default deploys —
			// keeps the request hot path one function call shorter
			// when admission is not configured.
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			decision := checker.Admit(r.Context())
			if decision.Allowed {
				next.ServeHTTP(w, r)
				return
			}
			emitAdmissionDeniedAudit(r, auditLogger, decision)
			writeAdmissionDenied(w, decision)
		})
	}
}

// writeAdmissionDenied stamps HTTP 402 with the Retry-After header
// (when ResetAt is in the future) and the structured body matching
// the platform's admission response shape. The Retry-After value is
// the integer number of seconds until ResetAt, clamped to non-
// negative so a stale ResetAt doesn't produce a negative header.
func writeAdmissionDenied(w http.ResponseWriter, d coreruntime.Decision) {
	w.Header().Set("Content-Type", "application/json")
	if !d.ResetAt.IsZero() {
		secs := time.Until(d.ResetAt).Seconds()
		if secs < 0 {
			secs = 0
		}
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(secs))))
	}
	w.WriteHeader(http.StatusPaymentRequired)
	body := admissionResponseBody{
		Error:  "admission_denied",
		Reason: d.Reason,
		Scope:  d.Scope,
		Window: d.Window,
	}
	if !d.ResetAt.IsZero() {
		body.ResetAt = d.ResetAt.UTC().Format(time.RFC3339)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// emitAdmissionDeniedAudit fires task_admission_denied with the
// platform-provided reason/scope/window plus the observability flag
// (`cached`) so SIEM dashboards can group denials by failure mode
// without joining against platform-side state. Goes through
// EmitFromContext so the per-invocation correlation_id + sequence
// counter + workflow / tenancy / entity stamps auto-attach from
// request context.
func emitAdmissionDeniedAudit(
	r *http.Request,
	logger *coreruntime.AuditLogger,
	d coreruntime.Decision,
) {
	if logger == nil {
		return
	}
	fields := map[string]any{
		"reason": d.Reason,
		"scope":  d.Scope,
		"window": d.Window,
		"cached": d.Cached,
	}
	if !d.ResetAt.IsZero() {
		fields["reset_at"] = d.ResetAt.UTC().Format(time.RFC3339)
	}
	logger.EmitFromContext(r.Context(), coreruntime.AuditEvent{
		Event:  coreruntime.AuditTaskAdmissionDenied,
		Fields: fields,
	})
}
