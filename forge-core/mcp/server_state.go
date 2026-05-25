package mcp

// validTransitions encodes the legal state machine for an mcp.Server.
// Drives the assertion in (*Server).transition — illegal transitions
// are programming bugs and panic in tests; in prod they force the
// Server into StateFailed so a misconfigured agent doesn't crash the
// whole process.
//
// All error paths route through StateDegraded:
//
//	{Connecting, Initializing, Discovering, Ready, Calling}
//	    └── err ──> Degraded ──> Reconnecting ──> Connecting ──> ...
//	                       └── (retries exhausted) ──> Failed
//
// "Degraded" means "we hit an error, deciding what to do next."
// "Reconnecting" means "we're in backoff, about to retry."
//
// Keep this in sync with the state diagram in docs/mcp/index.md.
var validTransitions = map[ServerState][]ServerState{
	StateConfigured:   {StateConnecting, StateStopped},
	StateConnecting:   {StateInitializing, StateDegraded, StateStopped},
	StateInitializing: {StateDiscovering, StateDegraded, StateStopped},
	StateDiscovering:  {StateReady, StateDegraded, StateStopped},
	StateReady:        {StateCalling, StateDegraded, StateStopped},
	StateCalling:      {StateReady, StateDegraded, StateStopped},
	StateDegraded:     {StateReconnecting, StateFailed, StateStopped},
	StateReconnecting: {StateConnecting, StateFailed, StateStopped},
	StateFailed:       {StateStopped},
	StateStopped:      {}, // terminal
}

// isValidTransition reports whether moving from→to is allowed.
func isValidTransition(from, to ServerState) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, t := range allowed {
		if t == to {
			return true
		}
	}
	return false
}
