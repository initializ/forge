package mcp

import "testing"

// TestIsValidTransition_Exhaustive walks every (from, to) pair and
// asserts the matrix matches the validTransitions table. Catches
// accidental drift if anyone reorganizes the map without thought.
func TestIsValidTransition_Exhaustive(t *testing.T) {
	t.Parallel()
	allStates := []ServerState{
		StateConfigured, StateConnecting, StateInitializing,
		StateDiscovering, StateReady, StateCalling,
		StateDegraded, StateReconnecting, StateFailed, StateStopped,
	}
	for _, from := range allStates {
		for _, to := range allStates {
			want := false
			for _, allowed := range validTransitions[from] {
				if allowed == to {
					want = true
					break
				}
			}
			got := isValidTransition(from, to)
			if got != want {
				t.Errorf("isValidTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

// TestIsValidTransition_StoppedIsTerminal pins the property that
// Stopped has no outbound transitions.
func TestIsValidTransition_StoppedIsTerminal(t *testing.T) {
	t.Parallel()
	allStates := []ServerState{
		StateConfigured, StateConnecting, StateInitializing,
		StateDiscovering, StateReady, StateCalling,
		StateDegraded, StateReconnecting, StateFailed, StateStopped,
	}
	for _, to := range allStates {
		if isValidTransition(StateStopped, to) {
			t.Errorf("Stopped→%s must be illegal (terminal state)", to)
		}
	}
}

// TestIsValidTransition_FailedToStoppedOnly pins the only outbound
// edge from Failed.
func TestIsValidTransition_FailedToStoppedOnly(t *testing.T) {
	t.Parallel()
	for _, to := range []ServerState{
		StateConfigured, StateConnecting, StateInitializing,
		StateDiscovering, StateReady, StateCalling,
		StateDegraded, StateReconnecting, StateFailed,
	} {
		if isValidTransition(StateFailed, to) {
			t.Errorf("Failed→%s must be illegal — only Failed→Stopped allowed", to)
		}
	}
	if !isValidTransition(StateFailed, StateStopped) {
		t.Errorf("Failed→Stopped must be legal")
	}
}
