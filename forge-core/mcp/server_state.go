package mcp

// validTransitions encodes the legal state machine for an mcp.Server.
// Drives the panic-loud assertion in (*Server).transition — illegal
// transitions are programming bugs, not runtime conditions.
//
// Keep this in sync with the state diagram in docs/mcp/index.md.
var validTransitions = map[ServerState][]ServerState{
	StateConfigured:   {StateConnecting, StateStopped},
	StateConnecting:   {StateInitializing, StateFailed, StateStopped},
	StateInitializing: {StateDiscovering, StateFailed, StateStopped},
	StateDiscovering:  {StateReady, StateFailed, StateStopped},
	StateReady:        {StateCalling, StateStopped, StateDegraded},
	StateCalling:      {StateReady, StateDegraded, StateStopped},
	StateDegraded:     {StateReconnecting, StateStopped},
	StateReconnecting: {StateInitializing, StateFailed, StateStopped},
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
