package owaspasi

import (
	"testing"

	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/tests/owasp-asi/graders"
)

// ASI08 — Cascading Failures. Grade: Partial.
//
// Enforced portion: the platform-policy engine bounds an agent's blast radius
// by denying egress domains via a layered union-of-deny, attributing the
// decision to the first denying layer. The instrumented signal is a
// policy_violation_at_build_time event carrying kind=denied_egress and the
// enforcing layer. Guideline: ASI08 #2 (trust boundaries).
func TestASI08_PolicyBoundsBlastRadius(t *testing.T) {
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{
			Mode:           "allowlist",
			AllowedDomains: []string{"api.openai.com", "evil.example.com"},
		},
	}
	layers := []security.PolicyLayer{
		{
			Source: security.LayerWorkspace,
			Path:   "/etc/forge/policy.yaml",
			Policy: security.PlatformPolicy{
				DeniedEgressDomains: []string{"evil.example.com"},
			},
		},
	}

	rec := graders.NewRecorder()
	violations := graders.EnforceAndRecord(rec, cfg, layers)

	if len(violations) == 0 {
		t.Fatal("expected a denied_egress violation, got none")
	}
	if !graders.PolicyViolationRecorded(rec, string(security.ViolationDeniedEgress), security.LayerWorkspace) {
		t.Errorf("no policy_violation_at_build_time attributed to layer %q", security.LayerWorkspace)
	}
	t.Logf("ASI08 policy engine denied %d domain(s), attributed to first denying layer", len(violations))
}

// TestASI08_ProgressCapTriggersCircuitBreaker is the failing target for the
// ASI08 backlog issue: single-agent blast-radius quotas / progress caps /
// circuit breakers between planner and executor do not exist yet.
func TestASI08_ProgressCapTriggersCircuitBreaker(t *testing.T) {
	t.Skip("xfail: GAP-CIRCUIT / issue #233 — no progress cap / circuit breaker " +
		"(ASI08 #7) implemented yet; this test defines the closure target.")
}
