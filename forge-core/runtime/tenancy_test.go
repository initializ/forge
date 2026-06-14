package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestTenancyContextFromHTTPHeaders covers the standard case (both
// headers present), the absent case (zero value), and the partial
// case (only one header present).
func TestTenancyContextFromHTTPHeaders(t *testing.T) {
	mkBoth := func() http.Header {
		h := http.Header{}
		h.Set(HeaderForgeOrgID, "org_abc")
		h.Set(HeaderForgeWorkspaceID, "ws_xyz")
		return h
	}
	mkOrgOnly := func() http.Header {
		h := http.Header{}
		h.Set(HeaderForgeOrgID, "org_abc")
		return h
	}
	tests := []struct {
		name string
		h    http.Header
		want TenancyContext
	}{
		{
			name: "both headers present",
			h:    mkBoth(),
			want: TenancyContext{OrgID: "org_abc", WorkspaceID: "ws_xyz"},
		},
		{
			name: "no headers → IsZero",
			h:    http.Header{},
			want: TenancyContext{},
		},
		{
			name: "only org → workspace omitted",
			h:    mkOrgOnly(),
			want: TenancyContext{OrgID: "org_abc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TenancyContextFromHTTPHeaders(tt.h)
			if got != tt.want {
				t.Errorf("TenancyContextFromHTTPHeaders = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestTenancyContext_IsZero verifies the zero-value detection that
// EmitFromContext relies on to know whether to fall back to the
// AuditLogger's static stamp.
func TestTenancyContext_IsZero(t *testing.T) {
	if !(TenancyContext{}).IsZero() {
		t.Error("empty TenancyContext should be IsZero")
	}
	if (TenancyContext{OrgID: "x"}).IsZero() {
		t.Error("TenancyContext with OrgID set should not be IsZero")
	}
	if (TenancyContext{WorkspaceID: "y"}).IsZero() {
		t.Error("TenancyContext with WorkspaceID set should not be IsZero")
	}
}

// TestAuditLogger_StaticTenancyStampsPlainEmit pins the deployment-
// time stamp behavior: WithTenancy installed once at startup, plain
// Emit (no ctx) lands org_id + workspace_id on the event. This is
// the startup-banner case (agent_card_published, policy_loaded).
func TestAuditLogger_StaticTenancyStampsPlainEmit(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithTenancy("org_static", "ws_static")
	al.Emit(AuditEvent{Event: "test_banner"})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrgID != "org_static" {
		t.Errorf("OrgID = %q, want org_static", got.OrgID)
	}
	if got.WorkspaceID != "ws_static" {
		t.Errorf("WorkspaceID = %q, want ws_static", got.WorkspaceID)
	}
}

// TestAuditLogger_NoTenancyStamp_OmitsFields confirms back-compat:
// without WithTenancy and without ctx override, the emitted JSON
// carries no org_id / workspace_id keys at all.
func TestAuditLogger_NoTenancyStamp_OmitsFields(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf)
	al.Emit(AuditEvent{Event: "test_banner"})

	out := buf.String()
	if strings.Contains(out, `"org_id"`) {
		t.Errorf("expected no org_id key, got: %s", out)
	}
	if strings.Contains(out, `"workspace_id"`) {
		t.Errorf("expected no workspace_id key, got: %s", out)
	}
}

// TestEmitFromContext_HeaderOverridesStaticStamp asserts the
// precedence rule: ctx-carried TenancyContext (from a per-request
// header) beats the AuditLogger's static stamp.
func TestEmitFromContext_HeaderOverridesStaticStamp(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithTenancy("org_env", "ws_env")
	ctx := WithTenancyContext(context.Background(), TenancyContext{
		OrgID:       "org_header",
		WorkspaceID: "ws_header",
	})
	al.EmitFromContext(ctx, AuditEvent{Event: "test_invocation"})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrgID != "org_header" {
		t.Errorf("OrgID = %q, want org_header (header should win)", got.OrgID)
	}
	if got.WorkspaceID != "ws_header" {
		t.Errorf("WorkspaceID = %q, want ws_header (header should win)", got.WorkspaceID)
	}
}

// TestEmitFromContext_PartialHeaderUsesStaticForOther verifies the
// independent-field behavior: a header that sets only OrgID lets
// the static stamp fill in WorkspaceID. Each field is resolved on
// its own merits.
func TestEmitFromContext_PartialHeaderUsesStaticForOther(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithTenancy("org_env", "ws_env")
	ctx := WithTenancyContext(context.Background(), TenancyContext{
		OrgID: "org_override",
	})
	al.EmitFromContext(ctx, AuditEvent{Event: "test_invocation"})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrgID != "org_override" {
		t.Errorf("OrgID = %q, want org_override", got.OrgID)
	}
	if got.WorkspaceID != "ws_env" {
		t.Errorf("WorkspaceID = %q, want ws_env (static fallback)", got.WorkspaceID)
	}
}

// TestEmitFromContext_ExplicitEventValueWins protects the
// "explicit-on-event beats every fallback" rule. Mirrors the same
// invariant EmitFromContext upholds for correlation_id, workflow_id,
// trace_id, etc.
func TestEmitFromContext_ExplicitEventValueWins(t *testing.T) {
	var buf bytes.Buffer
	al := NewAuditLogger(&buf).WithTenancy("org_env", "ws_env")
	ctx := WithTenancyContext(context.Background(), TenancyContext{
		OrgID:       "org_header",
		WorkspaceID: "ws_header",
	})
	al.EmitFromContext(ctx, AuditEvent{
		Event:       "test_invocation",
		OrgID:       "org_explicit",
		WorkspaceID: "ws_explicit",
	})

	var got AuditEvent
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrgID != "org_explicit" {
		t.Errorf("OrgID = %q, want org_explicit", got.OrgID)
	}
	if got.WorkspaceID != "ws_explicit" {
		t.Errorf("WorkspaceID = %q, want ws_explicit", got.WorkspaceID)
	}
}

// TestTenancyContext_ApplyToHTTPHeaders verifies the outbound
// propagation helper for agent-to-agent A2A flows.
func TestTenancyContext_ApplyToHTTPHeaders(t *testing.T) {
	h := http.Header{}
	TenancyContext{OrgID: "org_x", WorkspaceID: "ws_y"}.ApplyToHTTPHeaders(h)

	if got := h.Get(HeaderForgeOrgID); got != "org_x" {
		t.Errorf("OrgID header = %q, want org_x", got)
	}
	if got := h.Get(HeaderForgeWorkspaceID); got != "ws_y" {
		t.Errorf("WorkspaceID header = %q, want ws_y", got)
	}

	// Zero value writes nothing.
	hEmpty := http.Header{}
	TenancyContext{}.ApplyToHTTPHeaders(hEmpty)
	if len(hEmpty) != 0 {
		t.Errorf("expected zero-value TenancyContext to write no headers, got: %+v", hEmpty)
	}
}
