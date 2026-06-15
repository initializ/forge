package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestAuditLogger_StaticEntityStampsPlainEmit pins the deployment-
// time stamp behavior: WithEntity installed once at startup, plain
// Emit (no ctx) lands entity_id + entity_type on the event. This is
// the startup-banner case (agent_card_published, policy_loaded).
func TestAuditLogger_StaticEntityStampsPlainEmit(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithEntity("agent", "my-agent")
	al.Emit(AuditEvent{Event: "test_banner"})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EntityID != "my-agent" {
		t.Errorf("EntityID = %q, want my-agent", got.EntityID)
	}
	if got.EntityType != "agent" {
		t.Errorf("EntityType = %q, want agent", got.EntityType)
	}
}

// TestAuditLogger_NoEntityStamp_OmitsFields confirms back-compat:
// without WithEntity, the emitted JSON carries no entity_id /
// entity_type keys at all.
func TestAuditLogger_NoEntityStamp_OmitsFields(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf)
	al.Emit(AuditEvent{Event: "test_banner"})

	out := buf.String()
	if strings.Contains(out, `"entity_id"`) {
		t.Errorf("expected no entity_id key, got: %s", out)
	}
	if strings.Contains(out, `"entity_type"`) {
		t.Errorf("expected no entity_type key, got: %s", out)
	}
}

// TestEmitFromContext_StaticEntityStampsPerInvocationEvents verifies
// the stamp lands on EmitFromContext events too — per-invocation
// rows must carry entity_id alongside the request-scoped correlation
// fields, not just startup banners.
func TestEmitFromContext_StaticEntityStampsPerInvocationEvents(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithEntity("agent", "my-agent")

	ctx := WithCorrelationID(context.Background(), "corr-1")
	ctx = WithTaskID(ctx, "task-1")
	al.EmitFromContext(ctx, AuditEvent{Event: "test_invocation"})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EntityID != "my-agent" || got.EntityType != "agent" {
		t.Errorf("got (entity_id=%q, entity_type=%q), want (my-agent, agent)",
			got.EntityID, got.EntityType)
	}
	if got.CorrelationID != "corr-1" || got.TaskID != "task-1" {
		t.Errorf("correlation/task not preserved: %+v", got)
	}
}

// TestEmitFromContext_ExplicitEntityValueWins protects the
// "explicit-on-event beats every fallback" rule. Mirrors the same
// invariant EmitFromContext upholds for correlation_id, workflow_id,
// trace_id, and the tenancy keys.
func TestEmitFromContext_ExplicitEntityValueWins(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithEntity("agent", "my-agent")
	al.EmitFromContext(context.Background(), AuditEvent{
		Event:      "test_explicit",
		EntityID:   "explicit-entity",
		EntityType: "workflow",
	})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EntityID != "explicit-entity" {
		t.Errorf("EntityID = %q, want explicit-entity", got.EntityID)
	}
	if got.EntityType != "workflow" {
		t.Errorf("EntityType = %q, want workflow", got.EntityType)
	}
}

// TestAuditLogger_WithEntity_PartialStamp documents the per-field
// independence: setting only EntityID without EntityType (or vice
// versa) installs only that field on the logger. The omitempty tags
// drop the missing one in emitted JSON.
func TestAuditLogger_WithEntity_PartialStamp(t *testing.T) {
	var buf bytes.Buffer
	// EntityType empty, only EntityID set.
	al := NewAuditLogger(&buf).WithEntity("", "my-agent")
	al.Emit(AuditEvent{Event: "test_banner"})

	out := buf.String()
	if !strings.Contains(out, `"entity_id":"my-agent"`) {
		t.Errorf("expected entity_id=my-agent, got: %s", out)
	}
	if strings.Contains(out, `"entity_type"`) {
		t.Errorf("expected no entity_type key (empty), got: %s", out)
	}
}
