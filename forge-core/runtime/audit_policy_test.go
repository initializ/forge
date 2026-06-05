package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for issue #89 / FWS-5 — policy_loaded +
// policy_violation_at_build_time audit events. Emitted at startup so
// they don't have a request ctx; use Emit (not EmitFromContext).

func TestEmitPolicyLoaded_Shape(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitPolicyLoaded(map[string]any{
		"source":                 "/etc/forge/policy/platform-policy.yaml",
		"denied_egress_count":    3,
		"denied_tools_count":     2,
		"forbidden_models_count": 1,
		"max_egress_allowlist":   50,
		"max_tool_count":         100,
	})

	var evt AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if evt.Event != AuditPolicyLoaded {
		t.Errorf("Event = %q, want %q", evt.Event, AuditPolicyLoaded)
	}
	if evt.Fields["source"] != "/etc/forge/policy/platform-policy.yaml" {
		t.Errorf("source field not preserved, got %+v", evt.Fields)
	}
}

func TestEmitPolicyViolationAtBuildTime_Shape(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitPolicyViolationAtBuildTime(map[string]any{
		"violation_kind":   "denied_egress",
		"offending_value":  "api.slack.com",
		"forge_yaml_field": "egress.allowed_domains",
	})

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.Event != AuditPolicyViolationAtBuildTime {
		t.Errorf("Event = %q, want %q", evt.Event, AuditPolicyViolationAtBuildTime)
	}
	if evt.Fields["violation_kind"] != "denied_egress" {
		t.Errorf("violation_kind not preserved, got %+v", evt.Fields)
	}
	if evt.Fields["offending_value"] != "api.slack.com" {
		t.Errorf("offending_value not preserved, got %+v", evt.Fields)
	}
}

func TestEmitPolicyLoaded_NoCtxCorrelationFields(t *testing.T) {
	// Startup emits don't have a request ctx, so the workflow / task
	// correlation fields must NOT appear (they're optional with
	// omitempty, but lock in the byte shape so audit consumers reading
	// startup events don't get confused by absent fields).
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitPolicyLoaded(map[string]any{"source": "/x"})

	js := buf.String()
	for _, forbidden := range []string{`"correlation_id"`, `"task_id"`, `"workflow_id"`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("startup event should not carry %s, got: %s", forbidden, js)
		}
	}
}
