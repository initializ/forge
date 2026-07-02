package owaspasi

import (
	"context"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/tests/owasp-asi/graders"
)

// ASI10 — Rogue Agents. Grade: Partial.
//
// Enforced portion: the cancellation kill-switch produces an auditable record.
// When an invocation is cancelled (operator signal / tasks/cancel), the runtime
// emits invocation_cancelled carrying the reason. The instrumented signal is
// that audit event. Guideline: ASI10 #4 (containment/kill-switch).
func TestASI10_KillSwitchAudited(t *testing.T) {
	rec := graders.NewRecorder()
	rec.Logger.EmitInvocationCancelled(
		context.Background(),
		coreruntime.CancelReasonExternalSignal,
		50*time.Millisecond,
		map[string]any{"task_id": "task-123"},
	)

	if !rec.Has(coreruntime.AuditInvocationCancelled, "reason", string(coreruntime.CancelReasonExternalSignal)) {
		t.Errorf("kill-switch did not produce an invocation_cancelled audit event with the expected reason")
	}
	t.Log("ASI10 kill-switch: cancellation produced an auditable invocation_cancelled event")
}

// TestASI10_AuditChainTamperDetected is the failing target for issue #224:
// the audit stream has schema_version + monotonic seq but NO cryptographic
// integrity (no hash-chain / signature), so tampering is not detectable.
func TestASI10_AuditChainTamperDetected(t *testing.T) {
	t.Skip("xfail: GAP-AUDIT-SIGN / issue #224 — audit stream is not hash-chained " +
		"or signed (ASI10 #1); tamper-evidence is not implemented yet.")
}

// TestASI10_ManifestDeviationDetected is the failing target for issue #230:
// continuous behavioral-integrity checking against the declared SKILL.md/egress
// manifest is not implemented.
func TestASI10_ManifestDeviationDetected(t *testing.T) {
	t.Skip("xfail: GAP-INTEGRITY / issue #230 — no continuous manifest-deviation " +
		"detection (ASI10 #5/#6) implemented yet.")
}
