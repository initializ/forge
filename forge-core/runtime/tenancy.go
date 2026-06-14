package runtime

import (
	"context"
	"net/http"
)

// Tenancy header names (issue #157). The X-Forge- prefix is
// deliberate: these are Forge-defined override headers, distinct
// from the X-Org-ID / org_id headers the auth providers parse to
// resolve the user's identity. The auth-derived org_id continues
// to live in auth_verify.fields.org_id for back-compat; these
// headers populate the top-level audit fields that get stamped on
// EVERY event.
//
// Header semantics:
//
//   - Absent: the AuditLogger's deployment-time stamp wins (env
//     vars FORGE_ORG_ID / FORGE_WORKSPACE_ID resolved at startup).
//     This is the static-tenancy case — agent deployed into one
//     workspace, no per-request routing.
//   - Present: the header value overrides the env stamp for that
//     invocation. This is the multi-tenant case — one Forge agent
//     serves many workspaces, the orchestrator routes per request.
//
// Both: header wins. Neither: top-level fields are omitted entirely
// and emitted JSON matches the pre-tenancy shape.
const (
	HeaderForgeOrgID       = "X-Forge-Org-ID"
	HeaderForgeWorkspaceID = "X-Forge-Workspace-ID"
)

// TenancyContext carries the org / workspace identifiers a Forge
// agent extracts from inbound A2A request headers. Zero value is
// meaningful — it means "no per-request override; fall back to
// whatever the AuditLogger's static stamp says."
type TenancyContext struct {
	OrgID       string
	WorkspaceID string
}

// IsZero reports whether the TenancyContext carries no overrides.
// EmitFromContext checks this before reaching for the AuditLogger's
// static stamp.
func (t TenancyContext) IsZero() bool {
	return t.OrgID == "" && t.WorkspaceID == ""
}

// TenancyContextFromHTTPHeaders extracts X-Forge-Org-ID and
// X-Forge-Workspace-ID from an inbound HTTP request's headers.
// Missing headers map to empty fields; the returned TenancyContext
// is IsZero when neither is set. Mirrors WorkflowContextFromHTTPHeaders
// — same pattern, same precedence rules at the call site.
func TenancyContextFromHTTPHeaders(h http.Header) TenancyContext {
	return TenancyContext{
		OrgID:       h.Get(HeaderForgeOrgID),
		WorkspaceID: h.Get(HeaderForgeWorkspaceID),
	}
}

// ApplyToHTTPHeaders writes any non-empty TenancyContext fields
// onto outbound request headers. Used by tools that explicitly
// propagate tenancy to downstream A2A calls in an agent-to-agent
// flow. Auto-propagation is NOT built into the egress proxy — same
// rationale as WorkflowContext: a tenancy header would leak if the
// agent called a non-Forge third party. Tools propagate explicitly
// when they know the target is a tenancy-aware peer.
func (t TenancyContext) ApplyToHTTPHeaders(h http.Header) {
	if t.OrgID != "" {
		h.Set(HeaderForgeOrgID, t.OrgID)
	}
	if t.WorkspaceID != "" {
		h.Set(HeaderForgeWorkspaceID, t.WorkspaceID)
	}
}

// Context key for the TenancyContext. Unexported — callers go
// through WithTenancyContext / TenancyContextFromContext so the key
// type can never collide with another package's context key.
type tenancyContextKey struct{}

// WithTenancyContext stores a TenancyContext in the request context.
// Called at the A2A request boundary right after the workflow
// context is installed, so per-invocation handlers and the
// downstream audit emitters see both.
func WithTenancyContext(ctx context.Context, t TenancyContext) context.Context {
	return context.WithValue(ctx, tenancyContextKey{}, t)
}

// TenancyContextFromContext retrieves the TenancyContext from the
// context. Returns the zero value (IsZero == true) when none was
// set, which is the signal EmitFromContext uses to fall back to
// the AuditLogger's static tenancy stamp.
func TenancyContextFromContext(ctx context.Context) TenancyContext {
	if t, ok := ctx.Value(tenancyContextKey{}).(TenancyContext); ok {
		return t
	}
	return TenancyContext{}
}
