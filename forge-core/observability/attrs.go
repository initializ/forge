package observability

// Span attribute key constants Phase 3 (#104) instrumentation sites use.
// Centralized so dashboards / alerts in the operator's backend can be
// pinned to one source of truth — flipping an attribute name here is a
// breaking change and shows up in CI diffs.
//
// Two groups:
//
//  1. Semconv pass-throughs — names the OpenTelemetry community has
//     standardized. We re-declare them as constants here (rather than
//     importing semconv for every call site) so an upgrade of semconv
//     is a one-file mechanical sweep. The pinned-version constants
//     stay byte-identical to semconv until we choose to bump.
//
//  2. Forge-specific keys — namespaced under `forge.*` to avoid
//     collisions with vendor-supplied instrumentation. These don't
//     have a semconv equivalent (forge_task.id is a Forge concept,
//     not a generic OTel one).
//
// Span name conventions, separate from attributes, are documented at
// each instrumentation site rather than centralized here — a span name
// captures *what* operation is being measured ("a2a.tasks/send",
// "llm.completion") and reads naturally inline.

const (
	// ─── GenAI semconv (draft, pinned to OTel semconv 1.26.0 GenAI). ──
	// Backends like Honeycomb / Datadog / Grafana Tempo group LLM
	// activity by these. Naming follows the OTel GenAI spec exactly.

	// AttrGenAISystem identifies the LLM vendor: "anthropic",
	// "openai", "ollama", "openai-compatible".
	AttrGenAISystem = "gen_ai.system"

	// AttrGenAIRequestModel is the model the agent ASKED for.
	AttrGenAIRequestModel = "gen_ai.request.model"

	// AttrGenAIResponseModel is the model the vendor REPORTED back.
	// Often identical to the request, but enterprise routers can
	// substitute (e.g. Anthropic returns a versioned suffix).
	AttrGenAIResponseModel = "gen_ai.response.model"

	// AttrGenAIUsageInputTokens / AttrGenAIUsageOutputTokens — counted
	// directly from the provider's usage block. Drives cost
	// dashboards.
	AttrGenAIUsageInputTokens  = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens = "gen_ai.usage.output_tokens"

	// AttrGenAIResponseFinishReasons mirrors the Anthropic/OpenAI
	// "stop_reason" / "finish_reason" — "stop", "tool_use",
	// "max_tokens", "end_turn", etc.
	AttrGenAIResponseFinishReasons = "gen_ai.response.finish_reasons"

	// ─── Forge-specific attributes. ──────────────────────────────────

	// AttrForgeAgentID is the agent_id from forge.yaml — the operator's
	// primary identifier. Dashboards typically group by this first.
	AttrForgeAgentID = "forge.agent.id"

	// AttrForgeTaskID is the A2A task id (the same id surfaced in audit
	// events and the X-Forge-Task-Id response header). Lets operators
	// jump from an audit event to the corresponding trace.
	AttrForgeTaskID = "forge.task.id"

	// AttrForgeCorrelationID is the cross-process correlation id
	// (X-Forge-Correlation-Id header). Survives across the dispatcher
	// boundary so a span can be tied back to the inbound request.
	AttrForgeCorrelationID = "forge.correlation_id"

	// AttrForgeWorkflowID / Stage / Step are the FWS-2 orchestrator
	// correlation ids extracted from inbound X-Workflow-* headers.
	// Spans get these when the inbound request was issued by an
	// orchestrator (otherwise the keys are absent — backends should
	// treat missing keys as "ad-hoc invocation").
	AttrForgeWorkflowID      = "forge.workflow.id"
	AttrForgeWorkflowStageID = "forge.workflow.stage.id"
	AttrForgeWorkflowStepID  = "forge.workflow.step.id"

	// AttrForgeA2AMethod is the JSON-RPC method name on the inbound
	// span — "tasks/send", "tasks/get", "tasks/cancel". Span name is
	// also derived from this, but the explicit attribute simplifies
	// querying.
	AttrForgeA2AMethod = "forge.a2a.method"

	// AttrForgeLoopIteration is the executor iteration counter on the
	// agent.execute parent span — set after the loop finishes so
	// dashboards can chart "iterations per task."
	AttrForgeLoopIteration = "forge.loop.iteration"

	// AttrForgeToolName / AttrForgeToolError name the tool call
	// instrumentation.
	AttrForgeToolName  = "forge.tool.name"
	AttrForgeToolError = "forge.tool.error"

	// AttrForgeTaskFinalState is the terminal A2A TaskState the loop
	// resolved to — "completed", "failed", "canceled". Set on the
	// agent.execute span just before End.
	AttrForgeTaskFinalState = "forge.task.final_state"

	// ─── Content-capture attributes (Phase 3.5 / issue #130) ─────
	//
	// These attributes are set only when TracingConfig.CaptureContent
	// is true. The default posture remains metadata-only: an absent
	// attribute is the signal that an operator did not opt in. Set
	// values pass through PrepareSpanContent (redact-then-truncate)
	// so the same scrub passes both the OTel pipeline and (in the
	// future) the audit payload-capture path.

	// AttrGenAIInputMessages is the structured inbound message array
	// the agent sent to the LLM — a JSON array of role+content pairs.
	// Per OTel GenAI semantic conventions (current). Supersedes the
	// deprecated `gen_ai.prompt` flat-string attribute.
	AttrGenAIInputMessages = "gen_ai.input.messages"

	// AttrGenAIOutputMessages is the structured response array from
	// the model — a JSON array of role+content pairs (single element
	// for a non-streaming, single-choice completion). Per OTel GenAI
	// semantic conventions (current). Supersedes the deprecated
	// `gen_ai.completion` flat-string attribute.
	AttrGenAIOutputMessages = "gen_ai.output.messages"

	// AttrForgeToolArgs is the raw arguments JSON the agent passed to
	// a tool. Set on tool.<name> spans.
	AttrForgeToolArgs = "forge.tool.args"

	// AttrForgeToolResult is the raw output the tool returned. Set on
	// tool.<name> spans.
	AttrForgeToolResult = "forge.tool.result"

	// ─── Guardrail span attributes (issue #161) ──────────────────────
	//
	// Stamped on guardrail.<gate> spans the LibraryGuardrailEngine
	// opens around every Check* call (CheckInbound, CheckContext,
	// CheckToolCall, CheckToolOutput, CheckOutbound, CheckStream).
	// Symmetric to the guardrail_check audit-event fields shipped in
	// #156 / #160 — operators looking at a trace see the same gate /
	// decision / violation metadata they see in the audit stream and
	// can pivot between the two by correlation_id / trace_id without
	// joining on raw guardrail content.
	//
	// AttrForgeGuardrailEvidence follows the issue #130
	// CaptureContent + Redact + MaxBytes posture: default off, opt-in
	// per deployment, redact-then-truncate when on. Same env knobs as
	// the existing OTel content-capture pipeline.

	// AttrForgeGuardrailGate is the library gate that fired — one of
	// "input", "context", "tool_call", "output", "stream". Sourced
	// directly from `Result.Gate` (single source of truth, matches
	// fields.gate on the guardrail_check audit event).
	AttrForgeGuardrailGate = "forge.guardrail.gate"

	// AttrForgeGuardrailDecision is the library decision — one of
	// "allow", "mask", "block", "warn". Sourced from `Result.Decision`.
	AttrForgeGuardrailDecision = "forge.guardrail.decision"

	// AttrForgeGuardrailType is the first violation's Type field — e.g.
	// "pii", "moderation", "security". Omitted when violations list is
	// empty (the "allow" path).
	AttrForgeGuardrailType = "forge.guardrail.type"

	// AttrForgeGuardrailCategory is the first violation's Category —
	// e.g. "ssn", "email", "hate_speech". Omitted when the violation
	// has no category.
	AttrForgeGuardrailCategory = "forge.guardrail.category"

	// AttrForgeGuardrailViolationCount is the length of
	// `Result.Violations`. Useful for SIEM "show me high-violation
	// invocations" queries without joining against the full evidence
	// stream.
	AttrForgeGuardrailViolationCount = "forge.guardrail.violation_count"

	// AttrForgeGuardrailEvidence is the triggering content. Set only
	// when TracingConfig.CaptureContent is true. Passes through
	// PrepareSpanContent (redact-then-truncate) just like the other
	// #130 content attributes. For mask decisions evidence carries the
	// post-mask content (the same payload the LLM actually saw); for
	// block / warn decisions it carries the original triggering text
	// (the library never produces a masked variant in those paths).
	// Matches the audit-event evidence rule from PR #156.
	AttrForgeGuardrailEvidence = "forge.guardrail.evidence"
)
