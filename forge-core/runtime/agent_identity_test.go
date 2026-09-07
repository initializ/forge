package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Agent-identity L1–L4 (#444 item 2): the process-static agentic-identity
// stamp lands on every audit event, and the unpopulated promoted columns
// stay omitted (pre-#444 JSON shape) until their source flows land.

func TestAttestationLevelForMode(t *testing.T) {
	t.Run("k8s_sa → attested:placement", func(t *testing.T) {
		t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
		if got := AttestationLevelForMode(); got != AttestationPlacement {
			t.Errorf("AttestationLevelForMode() = %q, want %q", got, AttestationPlacement)
		}
	})
	t.Run("unset → empty", func(t *testing.T) {
		t.Setenv(EnvWorkloadIdentityMode, "")
		if got := AttestationLevelForMode(); got != "" {
			t.Errorf("AttestationLevelForMode() = %q, want \"\"", got)
		}
	})
	t.Run("other mode → empty (SPIRE is a later phase)", func(t *testing.T) {
		t.Setenv(EnvWorkloadIdentityMode, "attested_workload")
		if got := AttestationLevelForMode(); got != "" {
			t.Errorf("AttestationLevelForMode() = %q, want \"\"", got)
		}
	})
}

func TestAgentURN(t *testing.T) {
	if got := AgentURN("agt-1788"); got != "urn:agent:agt-1788" {
		t.Errorf("AgentURN() = %q, want urn:agent:agt-1788", got)
	}
	if got := AgentURN(""); got != "" {
		t.Errorf("AgentURN(\"\") = %q, want \"\" (no bare urn:agent: prefix)", got)
	}
}

func TestWithAgentIdentity_ClearsPhantomPrincipalUnderAgentOwn(t *testing.T) {
	// Insurance for items 3 / L2: even if a caller wrongly pairs a principal
	// with agent_own, the emitter clears it so no phantom principal reaches
	// the audit stream / PDP.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.WithAgentIdentity("urn:agent:x", AttestationPlacement, DelegationAgentOwn)
	audit.EmitFromContext(context.Background(), AuditEvent{
		Event:        AuditSessionStart,
		PrincipalSub: "user:should-be-cleared",
		PrincipalIss: "https://idp.example",
	})
	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.PrincipalSub != "" || evt.PrincipalIss != "" {
		t.Errorf("agent_own must clear principal_sub/iss, got sub=%q iss=%q", evt.PrincipalSub, evt.PrincipalIss)
	}
}

func TestWithAgentIdentity_StampsEveryEvent(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.WithAgentIdentity("agt-99", AttestationPlacement, DelegationAgentOwn)

	audit.EmitFromContext(context.Background(), AuditEvent{Event: AuditSessionStart})

	var evt AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if evt.ActorAgentID != "agt-99" {
		t.Errorf("actor_agent_id = %q, want agt-99", evt.ActorAgentID)
	}
	if evt.AttestationLevel != AttestationPlacement {
		t.Errorf("attestation_level = %q, want %q", evt.AttestationLevel, AttestationPlacement)
	}
	if evt.DelegationMode != DelegationAgentOwn {
		t.Errorf("delegation_mode = %q, want %q", evt.DelegationMode, DelegationAgentOwn)
	}
}

func TestWithAgentIdentity_UnpopulatedColumnsOmitted(t *testing.T) {
	// The plumb-only columns (no source until items 3 / L2) must NOT appear
	// in the JSON — a phantom principal_sub especially would trip the
	// platform's guard, and every unset column must preserve the pre-#444
	// wire shape.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.WithAgentIdentity("agt-99", "", DelegationAgentOwn) // non-k8s_sa: no attestation
	audit.EmitFromContext(context.Background(), AuditEvent{Event: AuditSessionStart})

	js := buf.String()
	for _, forbidden := range []string{
		`"principal_sub"`, `"principal_iss"`, `"actor_workload_id"`,
		`"mandate_id"`, `"grant_ref"`, `"chain_id"`, `"chain_hop"`,
		`"attestation_level"`, // empty (non-k8s_sa) → omitted
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("unpopulated column %s must be omitted, got: %s", forbidden, js)
		}
	}
	// agent_own must NOT carry a principal (phantom-principal invariant).
	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.PrincipalSub != "" {
		t.Errorf("principal_sub must be empty under agent_own, got %q", evt.PrincipalSub)
	}
}

func TestWithAgentIdentity_ExplicitEventValueWins(t *testing.T) {
	// An explicit per-event value (e.g. a delegated principal set by a later
	// item) must take precedence over the static stamp.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.WithAgentIdentity("agt-99", AttestationPlacement, DelegationAgentOwn)
	audit.EmitFromContext(context.Background(), AuditEvent{
		Event:          AuditSessionStart,
		DelegationMode: DelegationChained,
	})
	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.DelegationMode != DelegationChained {
		t.Errorf("explicit delegation_mode should win: got %q, want %q", evt.DelegationMode, DelegationChained)
	}
}
