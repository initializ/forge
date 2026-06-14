package runtime

import (
	"context"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
)

// GuardrailChecker validates messages, tool calls, retrieved context,
// and tool / LLM output against guardrail policies. Implementations
// may use file-based config, database-backed config, or no-op
// passthrough.
//
// Method names mirror the five gates the underlying guardrails
// library distinguishes (input / context / tool_call / output / stream)
// rather than the older inbound/outbound nomenclature. See issue #159.
//
// All Check methods accept a context so implementations can route
// audit emissions through AuditLogger.EmitFromContext and inherit
// correlation_id, task_id, sequence number, tenancy, and workflow
// tags from the request scope.
type GuardrailChecker interface {
	// CheckInbound validates an inbound (user) message — InputGate.
	CheckInbound(ctx context.Context, msg *a2a.Message) error

	// CheckOutbound validates an outbound (agent) message —
	// OutputGate. Implementations should prefer redacting sensitive
	// content over blocking.
	CheckOutbound(ctx context.Context, msg *a2a.Message) error

	// CheckToolCall validates the arguments the agent is about to
	// pass to a tool — ToolCallGate. Called from the BeforeToolExec
	// hook. Returns the (possibly redacted) args string and any
	// blocking error. Empty args short-circuit to (args, nil).
	CheckToolCall(ctx context.Context, toolName, args string) (string, error)

	// CheckToolOutput scans tool output text — OutputGate with tool
	// metadata so the emitted guardrail_check carries `tool` for
	// SIEM grouping. Returns the (possibly redacted) text.
	CheckToolOutput(ctx context.Context, toolName, text string) (string, error)

	// CheckContext validates retrieved context (RAG chunks, memory
	// recall, dynamic system-prompt content) before it is injected
	// into the LLM prompt — ContextGate. Returns the (possibly
	// redacted) content. Empty content short-circuits.
	//
	// The current Forge call site is the BeforeLLMCall hook, which
	// scans system-role messages assembled by the loop. Future memory
	// / RAG work can call this directly from the recall path when a
	// dedicated context-injection seam exists.
	CheckContext(ctx context.Context, content string) (string, error)

	// CheckStream validates a single chunk emitted by a streaming
	// LLM call — StreamGate. Returns the (possibly redacted) chunk.
	//
	// Forge's current Execute loop does not call provider streaming
	// (ExecuteStream is a buffered wrapper around non-streaming
	// Execute), so this is not auto-wired yet. The method is exposed
	// for callers that consume llm.Client.ChatStream directly and
	// for future loop work that adds a real per-chunk seam.
	CheckStream(ctx context.Context, chunk string) (string, error)
}

// NoopGuardrailChecker is a passthrough implementation that performs no checks.
// Used as a fallback when no guardrail configuration is available.
type NoopGuardrailChecker struct{}

func (n *NoopGuardrailChecker) CheckInbound(_ context.Context, _ *a2a.Message) error  { return nil }
func (n *NoopGuardrailChecker) CheckOutbound(_ context.Context, _ *a2a.Message) error { return nil }
func (n *NoopGuardrailChecker) CheckToolCall(_ context.Context, _, args string) (string, error) {
	return args, nil
}
func (n *NoopGuardrailChecker) CheckToolOutput(_ context.Context, _ string, text string) (string, error) {
	return text, nil
}
func (n *NoopGuardrailChecker) CheckContext(_ context.Context, content string) (string, error) {
	return content, nil
}
func (n *NoopGuardrailChecker) CheckStream(_ context.Context, chunk string) (string, error) {
	return chunk, nil
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
