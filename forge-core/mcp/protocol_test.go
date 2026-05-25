package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestProtocolVersion_Pinned guards against accidental version bumps.
// Bumping ProtocolVersion is a deliberate PR — this test forces that
// PR to also touch this test file.
func TestProtocolVersion_Pinned(t *testing.T) {
	t.Parallel()
	const want = "2025-06-18"
	if ProtocolVersion != want {
		t.Fatalf("ProtocolVersion = %q, want %q — bumping requires a deliberate PR touching tests and docs", ProtocolVersion, want)
	}
}

func TestJSONRPCMessage_Validate(t *testing.T) {
	t.Parallel()
	mustNum := func(s string) *json.Number { n := json.Number(s); return &n }

	cases := []struct {
		name    string
		msg     JSONRPCMessage
		wantErr bool
		wantSub string
	}{
		{
			name:    "valid request",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", ID: mustNum("1"), Method: "tools/list"},
			wantErr: false,
		},
		{
			name:    "valid notification (no id)",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", Method: "notifications/initialized"},
			wantErr: false,
		},
		{
			name:    "valid response — result",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", ID: mustNum("1"), Result: json.RawMessage(`{}`)},
			wantErr: false,
		},
		{
			name:    "valid response — error",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", ID: mustNum("1"), Error: &JSONRPCError{Code: -32600, Message: "x"}},
			wantErr: false,
		},
		{
			name:    "wrong jsonrpc version",
			msg:     JSONRPCMessage{Jsonrpc: "1.0", ID: mustNum("1"), Method: "x"},
			wantErr: true,
			wantSub: `jsonrpc field must be "2.0"`,
		},
		{
			name:    "response with both result and error",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", ID: mustNum("1"), Result: json.RawMessage(`{}`), Error: &JSONRPCError{Code: 1, Message: "x"}},
			wantErr: true,
			wantSub: "result OR error (xor)",
		},
		{
			name:    "response with neither result nor error",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", ID: mustNum("1")},
			wantErr: true,
			wantSub: "result OR error (xor)",
		},
		{
			name:    "empty frame",
			msg:     JSONRPCMessage{Jsonrpc: "2.0"},
			wantErr: true,
			wantSub: "empty frame",
		},
		{
			name:    "request carrying result is illegal",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", ID: mustNum("1"), Method: "x", Result: json.RawMessage(`{}`)},
			wantErr: true,
			wantSub: "request frame must not carry",
		},
		{
			name:    "notification carrying result is illegal",
			msg:     JSONRPCMessage{Jsonrpc: "2.0", Method: "x", Result: json.RawMessage(`{}`)},
			wantErr: true,
			wantSub: "notification frame must not carry",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.msg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !errors.Is(err, ErrProtocolError) {
					t.Errorf("err should wrap ErrProtocolError, got %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("err = %v, want substring %q", err, tc.wantSub)
				}
			}
		})
	}
}

// TestJSONRPCMessage_RawMessageFidelity ensures we don't lose fidelity
// round-tripping nested JSON. Critical for MCP tool input schemas
// that we hand to the LLM function-calling layer.
func TestJSONRPCMessage_RawMessageFidelity(t *testing.T) {
	t.Parallel()
	original := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"x","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}]}}`
	var msg JSONRPCMessage
	if err := json.Unmarshal([]byte(original), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"inputSchema":{"type":"object"`) {
		t.Fatalf("schema fidelity lost: %s", string(out))
	}
}
