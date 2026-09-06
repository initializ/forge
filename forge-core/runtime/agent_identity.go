package runtime

import "os"

// Agentic-identity vocabulary + derivation (agent-identity L1–L4, #444 item 2).
//
// These values are promoted onto audit events (security-next#41) and the PDP
// request so the platform's L2 delegation floors, the phantom-principal guard,
// mandate evaluation, and the L4 actor-breakdown / foreign-agent reports run on
// real data instead of nulls.

// Delegation modes (`del`). The string values MUST match security-next#42
// exactly — the PDP branches on them.
const (
	// DelegationChained — the action rode an agent-to-agent chain token.
	DelegationChained = "chained"
	// DelegationConnectedAccountUser — a per-user connected-account credential.
	DelegationConnectedAccountUser = "connected_account:user"
	// DelegationConnectedAccountWorkspace — a workspace-level connected account.
	DelegationConnectedAccountWorkspace = "connected_account:workspace"
	// DelegationMandate — action authorized by a mandate object.
	DelegationMandate = "mandate"
	// DelegationAgentOwn — the agent acts as its OWN principal, with no
	// delegated human/agent subject. This is forge's current PDP posture:
	// tool-call decisions are made as the agent principal (caller.subject =
	// agent:<id>), and no end-user subject is threaded into the decision.
	// A principal_sub MUST NOT accompany agent_own (that would be a phantom
	// principal); the delegated modes above are populated — together with a
	// principal_sub — by items 3 (chain) / L2 (connected-account, mandate)
	// as those flows land.
	DelegationAgentOwn = "agent_own"
	// DelegationNone — no delegation context at all.
	DelegationNone = "none"
)

// Attestation levels — how strongly the agent's workload identity is bound.
const (
	// AttestationPlacement — a k8s_sa projected ServiceAccount token: an
	// unbound bearer, trusted by pod placement + short TTL (#444 item 6,
	// security-next#42 Decision #7).
	AttestationPlacement = "attested:placement"
	// AttestationWorkload — a SPIRE X.509-SVID-bound token (future; item 6).
	AttestationWorkload = "attested:workload"
)

// AttestationLevelForMode derives the attestation level from the workload
// identity mode. Today only k8s_sa is handled → attested:placement; anything
// else (self-hosted / no workload identity) returns "" so the field is omitted.
// SPIRE mode (attested:workload) lands with #444 item 6.
func AttestationLevelForMode() string {
	if os.Getenv(EnvWorkloadIdentityMode) == WorkloadIdentityModeK8sSA {
		return AttestationPlacement
	}
	return ""
}
