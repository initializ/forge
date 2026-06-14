package runtime

import (
	"context"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
)

// GuardrailChecker validates messages and tool output against guardrail policies.
// Implementations may use file-based config, database-backed config, or no-op passthrough.
//
// All three Check methods accept a context so implementations can route audit
// emissions through AuditLogger.EmitFromContext and inherit correlation_id,
// task_id, and workflow-correlation tags from the inbound request scope.
type GuardrailChecker interface {
	// CheckInbound validates an inbound (user) message against guardrails.
	CheckInbound(ctx context.Context, msg *a2a.Message) error

	// CheckOutbound validates an outbound (agent) message against guardrails.
	// Implementations should prefer redacting sensitive content over blocking.
	CheckOutbound(ctx context.Context, msg *a2a.Message) error

	// CheckToolOutput scans tool output text against configured guardrails.
	// Returns the (possibly redacted) text and any blocking error.
	CheckToolOutput(ctx context.Context, toolName, text string) (string, error)
}

// NoopGuardrailChecker is a passthrough implementation that performs no checks.
// Used as a fallback when no guardrail configuration is available.
type NoopGuardrailChecker struct{}

func (n *NoopGuardrailChecker) CheckInbound(_ context.Context, _ *a2a.Message) error  { return nil }
func (n *NoopGuardrailChecker) CheckOutbound(_ context.Context, _ *a2a.Message) error { return nil }
func (n *NoopGuardrailChecker) CheckToolOutput(_ context.Context, _ string, text string) (string, error) {
	return text, nil
}

// ExtractText extracts all text parts from a message into a single string.
func ExtractText(msg *a2a.Message) string {
	var parts []string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, " ")
}
