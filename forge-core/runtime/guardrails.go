package runtime

import (
	"context"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
)

// PolicyDecision is the taxonomy of decisions a Forge policy engine
// (guardrails, admission, skill rules) can produce for one evaluated
// piece of content. Governance R4 requires the engine to be capable
// of expressing each of these — even if a particular gate only
// exercises a subset today. See docs/security/policy-decisions.md.
//
// Constants are ordered by RESTRICTIVENESS — the ordinal value maps
// to severity so `partA.Decision > partB.Decision` selects the more
// restrictive decision when aggregating across multiple parts:
//
//	Allow < Modify < StepUp < Defer < Deny
//
// Callers comparing severity SHOULD use the ordinal directly. Do NOT
// reorder without updating every aggregate site (see
// LibraryGuardrailEngine.CheckOutbound and its per-part escalation).
type PolicyDecision int

const (
	// DecisionAllow — content passes through unmodified. Zero value.
	// Least restrictive.
	DecisionAllow PolicyDecision = iota

	// DecisionModify — content is admissible but must be rewritten
	// (redacted, truncated, tagged) before it moves forward.
	// PolicyResult.Modified carries the replacement.
	DecisionModify

	// DecisionStepUp — content is conditionally admissible pending an
	// additional user/operator interaction (re-auth, approval,
	// verification). Reserved for R4b (#210).
	DecisionStepUp

	// DecisionDefer — decision requires an out-of-band lookup
	// (platform API, human queue) before the caller can proceed.
	// Reserved for R4c (#211).
	DecisionDefer

	// DecisionDeny — content is rejected. Caller MUST propagate the
	// error and MUST NOT let the content proceed. Most restrictive.
	DecisionDeny
)

// String returns the audit-safe decision token. Matches the strings
// emitted on guardrail_check events so a SIEM index built from those
// events can be queried by PolicyDecision value.
func (d PolicyDecision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	case DecisionModify:
		return "modify"
	case DecisionStepUp:
		return "step_up"
	case DecisionDefer:
		return "defer"
	default:
		return "unknown"
	}
}

// PolicyResult carries the outcome of one policy evaluation.
//
// For DecisionAllow / DecisionDeny / DecisionDefer, Modified is
// empty. For DecisionModify, Modified holds the rewritten content;
// callers substitute it into the value stream. For DecisionStepUp,
// RequiredAcr names the auth-context class the caller must re-authenticate
// under (see #210 / R4b). Reason is a short human-readable string
// surfaced on audit events and (for Deny/StepUp) returned to the caller
// as an error message.
type PolicyResult struct {
	Decision PolicyDecision
	Modified string
	Reason   string

	// RequiredAcr is populated only when Decision == DecisionStepUp
	// (governance R4b / #210). Names the auth-context class the
	// caller MUST re-authenticate under before the action is admitted.
	// The runner turns this into an RFC 9470 WWW-Authenticate challenge
	// on the 401 response: `Bearer error="step_up_required",
	// acr_values="<value>"`. Consumers (SDKs, browsers) trigger a
	// higher-assurance authentication and retry.
	//
	// Typical values follow the ACR conventions of the caller's IdP:
	//   - "acr:mfa"          — arbitrary MFA method acceptable
	//   - "urn:mace:incommon:iap:silver" — InCommon Silver
	//   - "0"/"1"/"2"        — SAML/oidc-style tier numbers
	// Forge doesn't interpret the value; it just relays it end-to-end
	// so operator + IdP + caller agree on the semantics.
	RequiredAcr string
}

// Allow constructs a passthrough result.
func Allow() PolicyResult { return PolicyResult{Decision: DecisionAllow} }

// Deny constructs a rejection result carrying `reason`.
func Deny(reason string) PolicyResult {
	return PolicyResult{Decision: DecisionDeny, Reason: reason}
}

// Modify constructs a redact-and-continue result.
func Modify(newContent, reason string) PolicyResult {
	return PolicyResult{Decision: DecisionModify, Modified: newContent, Reason: reason}
}

// StepUp constructs a step-up-required result. The `acr` is relayed
// to the caller in the RFC 9470 challenge header. See #210 / R4b.
func StepUp(requiredAcr, reason string) PolicyResult {
	return PolicyResult{Decision: DecisionStepUp, RequiredAcr: requiredAcr, Reason: reason}
}

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
	// Returns a PolicyResult carrying the engine's decision. On
	// DecisionModify the implementation SHOULD have already mutated
	// msg in place so the caller's downstream reads see the redacted
	// content; PolicyResult.Modified is provided for audit-trail use.
	// On DecisionDeny the caller MUST NOT admit the message.
	CheckInbound(ctx context.Context, msg *a2a.Message) (PolicyResult, error)

	// CheckOutbound validates an outbound (agent) message —
	// OutputGate. Same semantics as CheckInbound: MODIFY mutates
	// msg in place, DENY blocks.
	CheckOutbound(ctx context.Context, msg *a2a.Message) (PolicyResult, error)

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

func (n *NoopGuardrailChecker) CheckInbound(_ context.Context, _ *a2a.Message) (PolicyResult, error) {
	return Allow(), nil
}
func (n *NoopGuardrailChecker) CheckOutbound(_ context.Context, _ *a2a.Message) (PolicyResult, error) {
	return Allow(), nil
}
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
