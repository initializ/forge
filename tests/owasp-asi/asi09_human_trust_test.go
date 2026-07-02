package owaspasi

import "testing"

// ASI09 — Human-Agent Trust Exploitation. Grade: Partial.
//
// ASI09 is human-side: Forge can surface signals (guardrails, audit trail,
// egress provenance) but cannot fix human over-reliance. The buildable control
// is the same action-level approval / preview-vs-effect separation tracked for
// ASI02, deduplicated into issue #223.

// TestASI09_PreviewBlocksEffect is the failing target for issue #223:
// there is no preview mode that blocks state-changing tool calls before an
// explicit human confirmation (ASI09 #1/#7).
func TestASI09_PreviewBlocksEffect(t *testing.T) {
	t.Skip("xfail: GAP-HITL / issue #223 — no preview/effect separation or " +
		"action-level approval gate (ASI09 #1/#7) implemented yet.")
}
