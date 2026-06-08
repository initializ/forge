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
	// instrumentation. Tool args / results are NOT recorded here —
	// Phase 3 is metadata-only. A future "capture_content=true with
	// PII redaction" phase will add args/result attribute keys.
	AttrForgeToolName  = "forge.tool.name"
	AttrForgeToolError = "forge.tool.error"

	// AttrForgeTaskFinalState is the terminal A2A TaskState the loop
	// resolved to — "completed", "failed", "canceled". Set on the
	// agent.execute span just before End.
	AttrForgeTaskFinalState = "forge.task.final_state"
)
