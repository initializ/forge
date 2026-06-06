package runtime

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Regression tests for issue #90 / FWS-6 — channel deny audit events.
// After the three-layer redesign the only emitter is
// EmitChannelDeniedByPolicy; it carries channel + layer ("system" /
// "user" / "workspace") + source (the path of the deciding file).
// EmitChannelDisabledByConfig was removed when per-agent disable was
// ripped out of forge.yaml.

func TestEmitChannelDeniedByPolicy_Shape(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitChannelDeniedByPolicy("slack", "system", "/etc/forge/policy.yaml")

	var evt AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if evt.Event != AuditChannelDeniedByPolicy {
		t.Errorf("Event = %q, want %q", evt.Event, AuditChannelDeniedByPolicy)
	}
	if evt.Fields["channel"] != "slack" {
		t.Errorf("channel field: %+v", evt.Fields)
	}
	if evt.Fields["layer"] != "system" {
		t.Errorf("layer field: %+v", evt.Fields)
	}
	if evt.Fields["source"] != "/etc/forge/policy.yaml" {
		t.Errorf("source field: %+v", evt.Fields)
	}
}

// TestEmitChannelDeniedByPolicy_LayerAttribution locks in that each of
// the three layer values reaches the audit consumer verbatim. Audit
// pipelines group by layer to answer "which file is enforcing this?",
// so the values must pass through without case-folding or normalization.
func TestEmitChannelDeniedByPolicy_LayerAttribution(t *testing.T) {
	cases := []struct {
		layer string
		path  string
	}{
		{"system", "/etc/forge/policy.yaml"},
		{"user", "/home/dev/.forge/policy.yaml"},
		{"workspace", "/run/forge/workspace-policy.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.layer, func(t *testing.T) {
			var buf bytes.Buffer
			audit := NewAuditLogger(&buf)
			audit.EmitChannelDeniedByPolicy("telegram", tc.layer, tc.path)

			var evt AuditEvent
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
				t.Fatalf("decode: %v\n%s", err, buf.String())
			}
			if evt.Fields["layer"] != tc.layer {
				t.Errorf("layer = %v, want %q", evt.Fields["layer"], tc.layer)
			}
			if evt.Fields["source"] != tc.path {
				t.Errorf("source = %v, want %q", evt.Fields["source"], tc.path)
			}
		})
	}
}
