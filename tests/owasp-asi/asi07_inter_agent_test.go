package owaspasi

import "testing"

// ASI07 — Insecure Inter-Agent Communication. Grade: Partial / Deployer.
//
// Enforced/shipped controls (covered by forge-cli/server tests, not duplicated
// here): A2A 0.3.0 conformance, no inbound attack surface by default, optional
// auth middleware, per-IP rate limiting (a2a_server_ratelimit_test.go), and
// agent-card drift detection via card_sha256. See the conformance matrix.
//
// The gaps below are message-level integrity controls (forge-core/Platform),
// distinct from transport encryption (Deployer).

// TestASI07_ReplayedTaskRejected is the failing target for issue #226:
// no anti-replay nonce/timestamp bound to the task window exists.
func TestASI07_ReplayedTaskRejected(t *testing.T) {
	t.Skip("xfail: GAP-A2A-MSG / issue #226 — no anti-replay nonce/timestamp " +
		"(ASI07 #3); replayed task envelopes are not rejected.")
}

// TestASI07_SpoofedAgentCardRejected is the failing target for issue #226:
// no inter-agent message signing exists to reject a spoofed card at the
// message layer.
func TestASI07_SpoofedAgentCardRejected(t *testing.T) {
	t.Skip("xfail: GAP-A2A-MSG / issue #226 — no message-level signing " +
		"(ASI07 #2); message-layer spoof rejection is not implemented.")
}

// TestASI07_TransportEncryption is a Deployer responsibility (mTLS mesh +
// NetworkPolicy), not a forge-core control. Documented as xfail so the surface
// is enumerated, not hidden.
func TestASI07_TransportEncryption(t *testing.T) {
	t.Skip("xfail (Deployer): DEP-MTLS — inter-pod mTLS + NetworkPolicy are the " +
		"operator's job (ASI07 #1), not a forge-core gap.")
}
