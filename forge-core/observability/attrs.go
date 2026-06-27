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

	// AttrForgeWorkflow* are the FWS-2 orchestrator correlation ids
	// extracted from inbound X-Workflow-* headers. Spans get these
	// when the inbound request was issued by an orchestrator
	// (otherwise the keys are absent — backends should treat missing
	// keys as "ad-hoc invocation").
	//
	// AttrForgeWorkflowID is the workflow DEFINITION id (stable
	// across runs); AttrForgeWorkflowExecutionID is the per-run
	// instance id. FORGE-2 / issue #185 split — see the
	// HeaderWorkflow* docs in forge-core/runtime/workflow.go.
	AttrForgeWorkflowID          = "forge.workflow.id"
	AttrForgeWorkflowExecutionID = "forge.workflow.execution.id"
	AttrForgeWorkflowStageID     = "forge.workflow.stage.id"
	AttrForgeWorkflowStepID      = "forge.workflow.step.id"

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

	// ─── Auth verification span attributes (issue #187) ──────────────
	//
	// Stamped on auth.verify spans the auth middleware opens around
	// every Provider.Verify call. Symmetric to the auth_verify /
	// auth_fail audit-event fields so a SIEM looking at a trace sees
	// the same provider / token_kind / decision metadata it sees in
	// the audit stream — pivot between the two by trace_id without a
	// translation table.

	// AttrForgeAuthProvider names the chain entry that verified the
	// token — "oidc", "http_verifier", "gcp_iap", "aws_sigv4",
	// "static_token". Sourced from Identity.Source on success; empty
	// on the failure path (no provider claimed the token).
	AttrForgeAuthProvider = "forge.auth.provider"

	// AttrForgeAuthTokenKind is the post-verify structural classification
	// of the credential — "jwt", "opaque", "sigv4", "iap_jwt", "empty".
	// Matches the audit token_kind field exactly.
	AttrForgeAuthTokenKind = "forge.auth.token_kind"

	// AttrForgeAuthDecision is "verify" on success or "fail" on any
	// rejection path. The span Status carries the codes.Error marker on
	// fail; this attribute exists separately so SIEM consumers can
	// group on decision without parsing span status strings.
	AttrForgeAuthDecision = "forge.auth.decision"

	// AttrForgeAuthUserID / OrgID stamp the verified identity. Omitted
	// on the failure path. Useful for "auth latency per org" queries.
	AttrForgeAuthUserID = "forge.auth.user_id"
	AttrForgeAuthOrgID  = "forge.auth.org_id"

	// AttrForgeAuthFailReason is the classified failure reason for the
	// fail path — same vocabulary as the audit auth_fail.reason field
	// (e.g. "missing_token", "token_not_for_me", "token_rejected").
	// Omitted on the success path.
	AttrForgeAuthFailReason = "forge.auth.fail_reason"

	// ─── Channel adapter delivery span attributes (issue #187) ────────
	//
	// Stamped on channel.<adapter>.deliver spans the per-message handler
	// opens around the inbound-channel → internal-A2A-POST hop. Pairs
	// with traceparent injection on the internal POST so downstream
	// a2a.tasks/send spans nest under channel.<adapter>.deliver in the
	// flame graph.

	// AttrForgeChannelAdapter names the channel plugin that received
	// the message — "slack", "telegram", "msteams".
	AttrForgeChannelAdapter = "forge.channel.adapter"

	// AttrForgeChannelTarget identifies the conversational destination
	// — Slack channel ID / thread TS, Telegram chat ID, Teams chat
	// ID. Format is adapter-specific; a consumer pivots back to the
	// upstream system by joining on (channel.adapter, channel.target).
	AttrForgeChannelTarget = "forge.channel.target"

	// AttrForgeChannelMessageID is the upstream message identifier the
	// adapter received. Useful for the "find the trace for THIS Slack
	// message" pivot from a customer support ticket.
	AttrForgeChannelMessageID = "forge.channel.message_id"

	// AttrForgeChannelUserID is the upstream sender identity — Slack
	// U…, Telegram numeric ID, Teams AAD object ID. Useful for "auth
	// latency by user" queries.
	AttrForgeChannelUserID = "forge.channel.user_id"

	// ─── Scheduler fire span attributes (issue #187) ──────────────────
	//
	// Stamped on schedule.fire spans the file-backend scheduler opens
	// around every dispatch. Spans pair with the schedule_fire /
	// schedule_complete audit events so a SIEM joins them by trace_id.
	// K8s-backend dispatch is out of scope for v1 — the trigger Pod is
	// a separate curl-based Pod and needs traceparent injected into the
	// rendered CronJob YAML at `forge package` time.

	// AttrForgeScheduleID is the schedule identifier — Schedule.ID from
	// the registered Schedule struct.
	AttrForgeScheduleID = "forge.schedule.id"

	// AttrForgeScheduleCron is the cron expression that fired. Matches
	// the schedule_fire audit event's identifier and lets operators
	// build "fires per cron expression" rollups.
	AttrForgeScheduleCron = "forge.schedule.cron"

	// AttrForgeScheduleSource is either "yaml" (schedule from forge.yaml
	// at startup) or "llm" (schedule added at runtime by the agent's
	// LLM through the schedule_create tool). Sourced from Schedule.Source.
	AttrForgeScheduleSource = "forge.schedule.source"

	// ─── Admission span attributes (issue #201) ───────────────────────
	//
	// Stamped on admission.check spans the admission middleware opens
	// around the per-request platform admission call. Mirror the
	// task_admission_denied audit-event fields so a SIEM joins the
	// trace and the audit row by trace_id without translating
	// attribute names. The check runs once per inbound A2A request
	// (cached for 5s at steady state) so span volume is proportional
	// to inbound RPS, not cardinality of any quota-state dimension.

	// AttrForgeAdmissionDecision is "admit" or "deny" — the only two
	// values Forge consumes from the platform response. Anything else
	// → log warn + fail-open admit + admission.fallback=true.
	AttrForgeAdmissionDecision = "forge.admission.decision"

	// AttrForgeAdmissionReason is the platform-defined failure code
	// on deny — "cost_limit_exceeded", "billing_overdue",
	// "rate_limit_exhausted", … Empty on admit. Forge never enums
	// this; the vocabulary is the platform's.
	AttrForgeAdmissionReason = "forge.admission.reason"

	// AttrForgeAdmissionScope names which level in the platform's
	// billing hierarchy tripped — "agent" / "workspace" / "org" /
	// "". Purely informational for SRE runbook routing.
	AttrForgeAdmissionScope = "forge.admission.scope"

	// AttrForgeAdmissionWindow names the quota window that tripped —
	// "hourly", "daily", "monthly", "billing_cycle", … Platform-
	// defined. Lets dashboards build "denials by window type" rollups
	// without joining against platform state.
	AttrForgeAdmissionWindow = "forge.admission.window"

	// AttrForgeAdmissionCached is true when the decision came from
	// Forge's per-agent TTL cache, false on a fresh platform call.
	// Helps debug propagation lag — operators looking at why a
	// recently-recharged agent is still being denied check the
	// cached attribute first.
	AttrForgeAdmissionCached = "forge.admission.cached"

	// AttrForgeAdmissionFallback is true when an "admit" decision
	// was forced by a platform-call failure (timeout, 5xx, parse
	// error) rather than a real platform admit. Default is false.
	// Alerts on forge.admission.fallback=true surface platform
	// outage rate even though no caller observed it as a deny —
	// Forge fails open, so the platform's degraded state is
	// invisible at the caller boundary.
	AttrForgeAdmissionFallback = "forge.admission.fallback"

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
