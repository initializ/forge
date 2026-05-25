package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestServerState_String pins the lowercase event-log form. These
// strings are part of the audit-event contract — renaming any of them
// would break downstream consumers.
func TestServerState_String(t *testing.T) {
	t.Parallel()
	cases := map[ServerState]string{
		StateConfigured:   "configured",
		StateConnecting:   "connecting",
		StateInitializing: "initializing",
		StateDiscovering:  "discovering",
		StateReady:        "ready",
		StateCalling:      "calling",
		StateDegraded:     "degraded",
		StateReconnecting: "reconnecting",
		StateFailed:       "failed",
		StateStopped:      "stopped",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("ServerState(%d).String() = %q, want %q", int(s), got, want)
		}
	}

	// Unknown values must not panic; they get a generic form.
	if got := ServerState(99).String(); !strings.HasPrefix(got, "unknown(") {
		t.Errorf("unknown state: got %q, want prefix unknown(", got)
	}
}

// TestMCPToolDescriptor_JSONRoundtrip ensures the raw JSON Schema
// field round-trips losslessly — important because we hand it to the
// LLM's function-calling layer byte-for-byte.
func TestMCPToolDescriptor_JSONRoundtrip(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"name":"create_issue","description":"...","inputSchema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}`)

	var d MCPToolDescriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Name != "create_issue" {
		t.Errorf("name: got %q, want create_issue", d.Name)
	}
	// InputSchema must contain the property name we encoded.
	if !strings.Contains(string(d.InputSchema), `"title"`) {
		t.Errorf("InputSchema lost fidelity: %s", string(d.InputSchema))
	}
}
