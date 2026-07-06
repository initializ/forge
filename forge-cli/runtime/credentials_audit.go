package runtime

import (
	"context"

	"github.com/initializ/forge/forge-core/credentials"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// auditSinkAdapter is the credentials.AuditSink implementation that
// routes to the AuditLogger. It's a small shim so the credentials
// package doesn't depend on runtime (which would cycle: agentspec
// -> credentials -> runtime -> agentspec).
type auditSinkAdapter struct {
	logger *coreruntime.AuditLogger
}

// Compile-time assertion.
var _ credentials.AuditSink = (*auditSinkAdapter)(nil)

// Emit forwards each event through EmitFromContext so per-invocation
// correlation_id, task_id, sequence number, tenancy, and workflow
// tags auto-attach when the caller's ctx carries them.
func (a *auditSinkAdapter) Emit(ctx context.Context, event string, fields map[string]any) {
	if a == nil || a.logger == nil {
		return
	}
	a.logger.EmitFromContext(ctx, coreruntime.AuditEvent{
		Event:  event,
		Fields: fields,
	})
}
