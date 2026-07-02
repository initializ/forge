package graders

import (
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
)

// CheckEgress runs the REAL egress decision (security.DomainMatcher, the same
// matcher the runtime wires into its proxy and RoundTripper) for host under
// cfg, then emits the same egress_allowed / egress_blocked audit event the
// runner emits. It returns whether the host was allowed.
//
// This mirrors runner.go's OnAttempt wiring: the control is the matcher
// decision; the instrumented signal is the emitted audit event that the
// audit grader can then assert on.
func CheckEgress(rec *Recorder, cfg *security.EgressConfig, host string) bool {
	matcher := security.NewDomainMatcher(cfg.Mode, cfg.AllDomains)
	allowed := matcher.IsAllowed(host)
	event := coreruntime.AuditEgressBlocked
	if allowed {
		event = coreruntime.AuditEgressAllowed
	}
	rec.Logger.Emit(coreruntime.AuditEvent{
		Event: event,
		Fields: map[string]any{
			"domain": host,
			"mode":   string(cfg.Mode),
		},
	})
	return allowed
}

// EgressBlocked reports whether an egress_blocked event was recorded for host.
// This is the authoritative containment signal for exfil-attempt tests.
func EgressBlocked(rec *Recorder, host string) bool {
	return rec.Has(coreruntime.AuditEgressBlocked, "domain", host)
}
