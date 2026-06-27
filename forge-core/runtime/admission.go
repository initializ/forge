package runtime

import (
	"context"
	"time"
)

// AdmissionChecker decides whether a new inbound A2A invocation should
// be admitted. Used by the admission middleware (issue #201) to gate
// `tasks/send` (and any other write-method) traffic on a per-agent
// quota / cost ceiling that the platform owns.
//
// Forge itself never reasons about WHY a call is denied — it asks the
// platform a single yes-or-no question per agent and surfaces the
// answer to the caller. The platform's `Decision.Reason` /
// `Decision.Scope` / `Decision.Window` fields ride through to the
// caller's response body and to the `task_admission_denied` audit
// event so SIEM consumers can group by failure mode without joining
// against platform-side state.
//
// Admit MUST be safe to call concurrently; production checkers serve
// many in-flight A2A requests at once.
type AdmissionChecker interface {
	Admit(ctx context.Context) Decision
}

// Decision is the result of an admission check. Allowed=true means
// the invocation proceeds. Allowed=false means the middleware
// short-circuits with HTTP 402 and the other fields shape the
// response body + audit event + span attributes.
//
// Every non-Allowed field is platform-defined and opaque to Forge —
// they ride verbatim through the audit/span/response surface. The
// platform owns the vocabulary (e.g. `cost_limit_exceeded`,
// `billing_overdue`, `daily`, `monthly`). Forge never enums them.
//
// Cached + Fallback record observability metadata about how Forge
// reached the decision, not the decision content itself. They are
// stamped on the audit event and the OTel span so operators can
// distinguish a fresh platform "deny" from a cached one or a fail-
// open admit driven by a platform outage.
type Decision struct {
	// Allowed reports whether the invocation proceeds.
	Allowed bool

	// Reason is the platform's failure code on deny. Empty on admit.
	// Stable enough for SIEM grouping (`cost_limit_exceeded`,
	// `billing_overdue`, `rate_limit_exhausted`, …) — vocabulary
	// owned by the platform.
	Reason string

	// Scope names which level in the platform's billing hierarchy
	// tripped — `agent` / `workspace` / `org` / `""`. Purely
	// informational for audit + SRE runbook routing.
	Scope string

	// Window names the quota window that tripped — `hourly`,
	// `daily`, `monthly`, `billing_cycle`, … Platform-defined.
	// Lets the audit answer "was this a daily cap or a per-minute
	// burst?" without joining against platform state.
	Window string

	// ResetAt is when the deny clears (when the platform expects the
	// caller could retry successfully). Zero when unknown. Drives
	// the Retry-After header Forge stamps on the 402 response.
	ResetAt time.Time

	// Cached reports whether this decision came from Forge's local
	// per-agent TTL cache rather than a fresh platform call.
	// Reaches the audit event and span; helps operators debug
	// propagation lag when the platform "should have" already
	// re-admitted an agent.
	Cached bool

	// Fallback is true when Allowed=true was forced by a platform
	// call failure (timeout, 5xx, network error). The Decision is
	// indistinguishable from a real admit on the wire, but operators
	// alerting on `forge.admission.fallback=true` see the platform
	// outage rate even though no caller ever observed it as a deny.
	Fallback bool
}

// NoopAdmissionChecker is installed when the env-var pair
// (FORGE_ADMISSION_URL + FORGE_PLATFORM_TOKEN) is missing or
// incomplete. Every Admit returns an unconditional allow — the
// pre-#201 behavior. No platform call, no cache, no observability
// noise.
type NoopAdmissionChecker struct{}

// Admit always returns Allowed=true.
func (NoopAdmissionChecker) Admit(_ context.Context) Decision {
	return Decision{Allowed: true}
}
