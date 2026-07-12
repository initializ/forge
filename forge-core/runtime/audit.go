package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// AuditChainGenesis is the `prev_hash` value written on the very first
// audit event of a process. Downstream verifiers recognize this as
// "no predecessor" — sha256 (32 zero bytes) rendered in hex. See
// docs/security/audit-tamper-evidence.md (governance R5, issue #212).
const AuditChainGenesis = "0000000000000000000000000000000000000000000000000000000000000000"

// Audit event type constants.
const (
	AuditSessionStart     = "session_start"
	AuditSessionEnd       = "session_end"
	AuditToolExec         = "tool_exec"
	AuditEgressAllowed    = "egress_allowed"
	AuditEgressBlocked    = "egress_blocked"
	AuditLLMCall          = "llm_call"
	AuditGuardrail        = "guardrail_check"
	AuditScheduleFire     = "schedule_fire"
	AuditScheduleComplete = "schedule_complete"
	AuditScheduleSkip     = "schedule_skip"
	AuditScheduleModify   = "schedule_modify"

	// Auth events. Carry no PII (no email, no claims, no token bytes) —
	// only the audit subject (UserID), tenant (OrgID), and structural
	// metadata. See the auth-package middleware for the emitter.
	EventAuthVerify = "auth_verify" // every successful auth decision
	EventAuthFail   = "auth_fail"   // every failed auth decision (with reason code)

	// AuditAuthStepUpRequired is emitted when the R4b (#210) step-up
	// engine rejects a tool call because the caller's acr claim
	// doesn't satisfy the tool's required acr. Fields carry:
	//   - tool          : the tool name
	//   - required_acr  : the operator-declared required acr
	//   - presented_acr : the caller's current acr, or "" when absent
	//   - reason        : short human-readable classification
	// Payload never carries token bytes or full claim maps. The
	// runner also returns HTTP 401 with an RFC 9470
	// WWW-Authenticate challenge alongside emitting this event.
	// See docs/security/step-up-auth.md.
	AuditAuthStepUpRequired = "auth_step_up_required"

	// MCP events. Like auth, these carry NO byte payload — never the
	// arguments to a tool call, never the result content. Emitters
	// include only sizes (args_size, result_size), durations, server +
	// tool names, and reason codes. See forge-core/mcp and
	// forge-core/tools/adapters/mcp_tool.go.
	EventMCPServerStarted  = "mcp_server_started"
	EventMCPServerFailed   = "mcp_server_failed"
	EventMCPServerDegraded = "mcp_server_degraded"
	EventMCPToolCall       = "mcp_tool_call"
	EventMCPToolResult     = "mcp_tool_result"
	EventMCPToolConflict   = "mcp_tool_conflict"
	EventMCPTokenRefresh   = "mcp_token_refresh"

	// Agent Card events. Emitted once at agent startup with the
	// finalized A2A Agent Card content for traceability. Carries the
	// card's name, version, URL, protocolVersion, skill count, and a
	// sha256 hash of the JSON-encoded card so consumers can detect
	// config drift. See forge-cli/runtime/runner.go's startup pass
	// and the A2A 0.3.0 spec.
	EventAgentCardPublished = "agent_card_published"

	// Lifecycle events emitted at A2A invocation boundaries.
	// AuditInvocationComplete carries total wall-clock duration_ms for
	// the full invocation (auth → dispatch → engine.Execute → response).
	// See issue #87 / FWS-3.
	AuditInvocationComplete = "invocation_complete"

	// AuditLLMCallCancelled is emitted when a streaming LLM call is
	// cancelled mid-flight; carries partial usage counts captured up to
	// the cancellation point. See issue #87 / FWS-3.
	AuditLLMCallCancelled = "llm_call_cancelled"

	// Credential events (governance R9). Emitted per BeforeToolExec
	// when a JIT credential is materialized for a tool call, and again
	// on AfterToolExec when a revocable credential is revoked. Fields:
	// provider (plugin name), tool, ttl, scope. Payloads never carry
	// the credential material itself — only its metadata.
	AuditCredentialIssued  = "credential_issued"
	AuditCredentialRevoked = "credential_revoked"
	AuditCredentialFailed  = "credential_failed"

	// AuditIntentAlignment is emitted per BeforeToolExec when the R3
	// intent-alignment engine (#208) is enabled. Fields carry:
	//   - tool     : the tool name being scored
	//   - score    : cosine similarity ∈ [-1, 1] (or "NaN" on error)
	//   - decision : "allow" / "warn" / "deny"
	//   - reason   : short human-readable classification
	// Payload never carries the LLM prompt or tool args — only the
	// scored decision. See docs/security/intent-alignment.md.
	AuditIntentAlignment = "intent_alignment"

	// AuditIntentDrift is emitted when the R7 (#214) rolling-window
	// drift analyzer detects a state transition — either the task
	// entered drift (mean-below-threshold OR monotone-decrease) or
	// recovered. State-transition semantics keep the audit stream
	// from flooding during a long drift stretch: one event on entry,
	// one on recovery.
	//
	// Fields:
	//   - tool       : the tool call that tripped the transition
	//   - severity   : "mean_below_threshold" | "monotone_decrease" |
	//                  "both" | "recovered"
	//   - transition : "entered" | "recovered"
	//   - mean       : rolling-window mean at trip time
	//   - window     : the configured window size
	// Payload never carries the individual scores. See
	// docs/security/intent-alignment.md (drift section).
	AuditIntentDrift = "intent_drift"

	// Defer events (governance R4c / #211). Three-stage lifecycle:
	//   1. AuditTaskDeferred          — hook paused the executor,
	//                                    task flipped to `deferred`
	//                                    state. Fields: tool, to,
	//                                    timeout_ms, context (truncated).
	//   2. AuditTaskDeferredDecision  — POST /tasks/{id}/decisions
	//                                    arrived. Fields: tool,
	//                                    decision (approve|reject),
	//                                    approver, note, wait_ms.
	//   3. AuditTaskDeferredTimeout   — no decision arrived within
	//                                    timeout, auto-DENYing.
	//                                    Fields: tool, timeout_ms.
	// Payload never carries token bytes or full LLM messages.
	AuditTaskDeferred         = "task_deferred"
	AuditTaskDeferredDecision = "task_deferred_decision"
	AuditTaskDeferredTimeout  = "task_deferred_timeout"

	// AuditPolicyLoaded is emitted once at agent startup when a
	// non-zero platform policy is present. Carries a summary of the
	// effective policy (sizes of deny lists, max bounds) so audit
	// consumers can confirm which policy was active during a given
	// run without parsing the policy file itself. Absent when no
	// policy is configured. See issue #89 / FWS-5.
	AuditPolicyLoaded = "policy_loaded"

	// AuditPolicyViolationAtBuildTime is emitted when forge.yaml's
	// declaration conflicts with the platform policy at startup
	// (e.g., declares a domain on the policy deny list, declares a
	// forbidden model, exceeds size bounds). Carries the conflict
	// detail in Fields. Emitted ONCE per startup before the runner
	// aborts with a non-zero exit, so the violation lands in the
	// audit pipeline even though the agent never serves traffic.
	// See issue #89 / FWS-5.
	AuditPolicyViolationAtBuildTime = "policy_violation_at_build_time"

	// AuditChannelDeniedByPolicy is emitted at agent startup when a
	// channel adapter would have been started but a policy layer
	// (system / user / workspace) names it on its denied_channels
	// list. The channel is NOT started; the runner continues with
	// the remaining channels rather than aborting — unlike the
	// egress/tool/model violations, channel deny is treated as a
	// scope-down, not as a forge.yaml conflict.
	//
	// Carries fields.channel (registry name), fields.layer
	// ("system" / "user" / "workspace") identifying which file
	// enforced, and fields.source (path to that file). When the
	// channel is denied by multiple layers, the first-match wins
	// for attribution (system > user > workspace precedence; the
	// most restrictive layer takes credit).
	//
	// See issue #90 / FWS-6.
	AuditChannelDeniedByPolicy = "channel_denied_by_policy"

	// AuditInvocationCancelled is emitted when an in-flight A2A
	// invocation is cancelled by tasks/cancel (or internal cancellation
	// like a parent ctx deadline). Carries the classified reason in
	// Fields["reason"], the wall-clock duration up to cancellation in
	// DurationMs, and aggregated partial usage in Fields when any LLM
	// calls completed before the cancel signal. See issue #88 / FWS-4.
	AuditInvocationCancelled = "invocation_cancelled"

	// AuditTaskAdmissionDenied is emitted when the admission middleware
	// rejects an inbound A2A invocation based on a platform-side quota
	// / cost-limit decision (issue #201). Carries the platform's
	// classification in Fields:
	//
	//   - reason  : platform-defined failure code
	//                ("cost_limit_exceeded", "billing_overdue", …)
	//   - scope   : which level in the platform's hierarchy tripped
	//                ("agent" / "workspace" / "org")
	//   - window  : which quota window tripped
	//                ("hourly" / "daily" / "monthly" / "billing_cycle")
	//   - reset_at: RFC 3339 timestamp when the deny clears, also used
	//                to derive the caller's Retry-After header
	//   - cached  : true when the decision came from Forge's per-agent
	//                TTL cache; false on a fresh platform call. Lets
	//                operators distinguish "platform actively denied"
	//                from "serving a few-second-old cached deny" when
	//                debugging propagation lag.
	//
	// Caller observes HTTP 402 Payment Required with the same shape as
	// the audit fields surfaced in the response body. Distinct from
	// auth_fail (which signals authentication failure, HTTP 401) and
	// from rate limit drops (which signal request-rate ceilings, HTTP
	// 429). See docs/security/admission.md.
	AuditTaskAdmissionDenied = "task_admission_denied"

	// Deprecated: use EventAuthVerify. Kept as a string alias so any
	// audit-log consumer that grep'd for "auth_success" can be migrated.
	// Scheduled for removal in v0.11.0.
	AuditAuthSuccess = EventAuthVerify
	// Deprecated: use EventAuthFail. Same migration window as AuditAuthSuccess.
	AuditAuthFailure = EventAuthFail
)

// AuditEvent is a single structured audit record emitted as NDJSON.
//
// Workflow correlation fields (WorkflowID, WorkflowExecutionID,
// StageID, StepID, InvocationCaller) are tagged onto every event
// emitted via EmitFromContext when the request carries `X-Workflow-*`
// / `X-Invocation-Caller` headers from any A2A-compatible
// orchestrator. Direct A2A invocations omit them entirely so the JSON
// shape matches the pre-FWS-2 audit consumers.
//
// WorkflowID / WorkflowExecutionID split (FORGE-2 / issue #185):
// `workflow_id` carries the workflow DEFINITION id (stable across all
// runs), `workflow_execution_id` carries the PER-RUN instance id.
// Audit consumers join on `workflow_execution_id` for per-run
// timelines and on `workflow_id` for definition-level rollups.
//
// Token usage, duration, model, and provider fields (issue #87 / FWS-3)
// are populated by the LLM call site, tool execution path, and per-
// invocation lifecycle. They use *int / *int64 pointers so the JSON
// distinguishes "field absent" (nil) from "field present with zero
// value" — important for llm_call events where zero is a legitimate
// count and TokensUnavailable signals "provider did not report usage."
//
// Field naming aligns with OTel GenAI semconv (input_tokens /
// output_tokens / duration_ms) so audit consumers can correlate Forge
// audit events with OTel traces without a translation table.
type AuditEvent struct {
	Timestamp string `json:"ts"`
	Event     string `json:"event"`

	// SchemaVersion advertises the audit-event contract version every
	// emitted event conforms to. Consumers (initializ platform,
	// custom SIEM pipelines) read this once per agent run to detect
	// schema upgrades. Backward-compatible additions to the schema
	// do not bump the version; removals or semantic changes do.
	// See docs/security/audit-logging.md#schema-contract-fws-8.
	SchemaVersion string `json:"schema_version,omitempty"`

	// Sequence is a per-invocation monotonically increasing counter.
	// Starts at 1 for the first event of an invocation; advances by
	// 1 on each subsequent event from that invocation. Consumers
	// detect gaps (lost / out-of-order events) by comparing
	// Sequence values within a (correlation_id, task_id) group.
	//
	// Sequences are scoped to a single A2A invocation — different
	// invocations start their own counters. Events emitted outside
	// any invocation scope (startup events: policy_loaded,
	// agent_card_published, audit_export_status) have no Sequence
	// and omit the field.
	//
	// See issue #91 / FWS-8.
	Sequence int64 `json:"seq,omitempty"`

	// PrevHash carries the sha256 of the previous event's line bytes
	// on the sink stream (raw line without the trailing newline).
	// Together with the per-emit tail-hash update this forms a hash
	// chain over the stream — any post-hoc alteration to a prior
	// event breaks the chain at that point.
	//
	// The very first event of the AuditLogger's lifetime carries
	// AuditChainGenesis ("00…00" — 32 zero bytes hex-encoded); verifiers
	// treat that value as "no predecessor" and start their walk there.
	// The field is written on EVERY event (no omitempty) — its absence
	// is itself a tampering signal.
	//
	// PrevHash is covered by the Ed25519 signature (Sig) because the
	// signature is computed after PrevHash is stamped and with only
	// Sig blanked — so tampering with prev_hash breaks both the chain
	// verification AND the signature verification. See #212 (R5) and
	// #213 (R6) / docs/security/audit-tamper-evidence.md.
	PrevHash string `json:"prev_hash"`

	// Sigp identifies the canonicalization scheme used to produce the
	// bytes over which Sig is computed. Present iff Sig is present.
	// Currently one value:
	//
	//   "jcs-1" — RFC 8785 (JCS) applied to the event with `sig`
	//             removed. Numbers are ES6-formatted; object keys are
	//             UTF-16-lexicographic-sorted; strings use minimal
	//             RFC 8259 escaping; no whitespace.
	//
	// Marked on the wire so verifiers know exactly which
	// canonicalization to apply, and so future schemes can be added
	// without confusion. The signature covers Sigp itself
	// (canonicalize is called after Sigp is stamped, with only Sig
	// blanked), so a tamperer can't downgrade the scheme.
	Sigp string `json:"sigp,omitempty"`

	// Kid identifies the audit signing key used to produce Sig.
	// Consumers cross-reference it against the JWKS served at
	// /.well-known/forge-audit-keys (or an out-of-band published
	// keyset) to fetch the matching pubkey. Present iff the Forge
	// deployment has audit signing enabled — otherwise absent.
	// See docs/security/audit-signing.md (#213 / governance R6).
	Kid string `json:"kid,omitempty"`

	// Sig is the base64-encoded Ed25519 signature over the canonical
	// preimage of this event with Sig itself empty. The preimage
	// canonicalization is identified by Sigp (currently "jcs-1" — RFC
	// 8785 JCS). Using JCS lets non-Go verifiers compute the preimage
	// with any spec-compliant library, avoiding the "reproduce Go's
	// encoding/json quirks" burden. It also sidesteps the large-int
	// precision hole where json.Marshal(json.Unmarshal(x)) isn't a
	// fixed point (JSON numbers decode to float64; JCS carries all
	// numbers through the same ES6-double rule on both sides).
	//
	// The signature covers every other field including Sigp, Kid, and
	// PrevHash — tampering with any of them (including the chain
	// link or the canonicalization scheme) is detected at verify time.
	//
	// Present iff Kid is set. Absent (and never emitted) when
	// audit signing is off. See #213 / governance R6.
	Sig string `json:"sig,omitempty"`

	// CorrelationID groups events from a single agent invocation —
	// generated by the A2A handler at request entry.
	CorrelationID string `json:"correlation_id,omitempty"`

	// TaskID is the A2A task identifier (params.id on tasks/send).
	TaskID string `json:"task_id,omitempty"`

	// WorkflowID identifies the workflow DEFINITION that invoked this
	// agent. Stable across every run of the same workflow. Sourced
	// from X-Workflow-ID at request entry; absent for direct A2A
	// invocations. SIEM consumers join on this for definition-level
	// rollups ("top failing workflows"). FORGE-2 / issue #185 split.
	WorkflowID string `json:"workflow_id,omitempty"`

	// WorkflowExecutionID identifies the per-run instance of the
	// workflow that invoked this agent. Unique per workflow
	// execution. Sourced from X-Workflow-Execution-ID at request
	// entry; absent for direct A2A invocations. SIEM consumers join
	// on this for per-run timelines ("every event in this specific
	// run, across every agent the orchestrator dispatched to"). Added
	// in FORGE-2 / issue #185.
	WorkflowExecutionID string `json:"workflow_execution_id,omitempty"`

	// StageID identifies the workflow stage that invoked this agent.
	StageID string `json:"stage_id,omitempty"`

	// StepID identifies the workflow step that invoked this agent.
	StepID string `json:"step_id,omitempty"`

	// InvocationCaller identifies the upstream caller (orchestrator
	// or upstream agent in an agent-to-agent flow).
	InvocationCaller string `json:"invocation_caller,omitempty"`

	// OrgID + WorkspaceID stamp the tenancy this agent run belongs
	// to. Sourced from one of three layers (highest precedence first):
	//
	//   1. Explicit value set on the event before emit.
	//   2. Per-request override headers parsed at the A2A boundary
	//      (X-Forge-Org-ID / X-Forge-Workspace-ID) and stashed on the
	//      context via WithTenancyContext.
	//   3. Deployment-time stamp installed on the AuditLogger via
	//      WithTenancy(orgID, workspaceID) — typically populated from
	//      FORGE_ORG_ID / FORGE_WORKSPACE_ID at agent startup.
	//
	// Both keys use omitempty so deployments that don't set tenancy
	// keep emitting the pre-tenancy JSON shape verbatim. The
	// AuditSchemaVersion is NOT bumped — additive optional fields are
	// schema-compatible per the documented policy. See issue #157.
	//
	// Distinct from the auth-derived `auth_verify.fields.org_id`,
	// which continues to carry whatever the inbound token claimed.
	// The top-level OrgID here is the operator's declared tenancy,
	// trusted because the deployment / orchestrator set it.
	OrgID       string `json:"org_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`

	// EntityID + EntityType identify which entity emitted this event.
	// Sourced from two layers (highest precedence first):
	//
	//   1. Explicit value set on the event before emit.
	//   2. Deployment-time stamp installed on the AuditLogger via
	//      WithEntity(entityType, entityID) — typically populated from
	//      FORGE_AGENT_ID / cfg.AgentID at agent startup, with
	//      EntityType hardcoded to "agent" for now.
	//
	// No per-request ctx layer: entity identity is fixed at process
	// startup. If an agent serves multiple tenancies per request, the
	// OrgID / WorkspaceID layer above already covers that.
	//
	// Field names + values match the guardrails library's BasePayload
	// vocabulary (EntityID, EntityType — "agent" / "workflow" /
	// "assistant") so the Forge NDJSON stream lines up with the
	// library's own vocabulary without a translation table. EntityType
	// is hardcoded to "agent" today since Forge only runs agents;
	// future entity types are an additive value change, not a schema
	// change.
	//
	// Both keys use omitempty so deployments that don't set agent_id
	// keep emitting the pre-#164 JSON shape verbatim.
	EntityID   string `json:"entity_id,omitempty"`
	EntityType string `json:"entity_type,omitempty"`

	// LLM call attribution (llm_call, llm_call_cancelled, invocation_complete).
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`

	// Token counts captured from provider response metadata. Nil when
	// the event is not an LLM call. Non-nil with zero values + a true
	// TokensUnavailable flag when the provider did not return usage
	// (e.g. some self-hosted Ollama setups).
	InputTokens       *int `json:"input_tokens,omitempty"`
	OutputTokens      *int `json:"output_tokens,omitempty"`
	TokensUnavailable bool `json:"tokens_unavailable,omitempty"`

	// DurationMs is the wall-clock duration in milliseconds. Populated on
	// llm_call, tool_exec, and invocation_complete events.
	DurationMs *int64 `json:"duration_ms,omitempty"`

	// RequestID is the provider-specific call identifier (Anthropic
	// `id`, OpenAI `id`, etc.) — kept as an opaque debug-correlation
	// handle, never used for cost attribution.
	RequestID string `json:"request_id,omitempty"`

	// TraceID + SpanID cross-link this audit event to the OTel trace
	// the same logical operation produced (Phase 4 of the OTel
	// Tracing v1 initiative — issue #105 / #108). Populated by
	// EmitFromContext when the context carries a recording span;
	// omitted when there is no span on the context or the tracer is
	// the noop default (tracing disabled).
	//
	// Format: lowercase hex, matching W3C traceparent semantics —
	// trace_id is 32 hex chars (128-bit), span_id is 16 hex chars
	// (64-bit). Operators paste these directly into their trace
	// backend's search box to pivot from an audit row to the parent
	// trace, and vice versa.
	//
	// Backward compatibility: both fields use omitempty so consumers
	// that have not been upgraded continue to see the pre-Phase-4
	// shape verbatim (no trace_id / span_id keys at all). The
	// AuditSchemaVersion is NOT bumped — adding optional fields is a
	// schema-compatible change per the documented policy.
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`

	Fields map[string]any `json:"fields,omitempty"`
}

// AuditLogger fans serialized NDJSON audit events out to a slice of
// Sinks. The traditional single-writer constructor wraps the writer in
// a writerSink; the FWS-7 multi-sink constructor (NewAuditLoggerFromConfig)
// composes a stderr safety-net sink with an optional Unix socket or
// localhost HTTP sink for export to a sidecar.
//
// Emit-side semantics:
//   - Each sink's Write is called sequentially. Each sink is responsible
//     for its own timeout/drop behavior; the AuditLogger never spawns a
//     goroutine per event. This bounds emit latency to the sum of sink
//     timeouts (stderr is microseconds; socket/HTTP is the configured
//     per-write timeout, default 50ms).
//   - Errors from a sink are logged once per (sink, error-class) and
//     suppressed thereafter — a broken sidecar must not flood the
//     operational logs.
//   - Events leaving each sink are byte-identical. No sink transforms
//     the payload.
type AuditLogger struct {
	mu      sync.Mutex
	sinks   []Sink
	logOnce map[string]bool // sink_name → first-error-already-logged for that sink
	opsLog  Logger          // optional structured logger for sink-error reporting; nil disables

	// signer signs every emitted event when non-nil. Nil signer =
	// signing off (pre-#213 behavior; Sig + Kid absent from output).
	// Configured once at startup via SetSigner from the loaded key.
	signer *AuditSigner

	// lastHash is the sha256 (hex-encoded) of the previous event's
	// raw line bytes (as they went to the sink, WITHOUT the trailing
	// newline). Each Emit stamps event.PrevHash = lastHash then
	// updates lastHash to sha256(current event's line bytes). The
	// very first Emit sees lastHash == "" and writes AuditChainGenesis
	// so downstream verifiers have a well-defined start value.
	//
	// Guarded by mu; the chain-mint + sign + marshal + hash + sink-write
	// sequence executes under the same lock so events land on disk in
	// the same order they were chained. See #212 (R5).
	lastHash string

	// Static tenancy stamp, installed once at agent startup via
	// WithTenancy(). Populated from FORGE_ORG_ID / FORGE_WORKSPACE_ID
	// in the CLI runner. EmitFromContext falls back to these whenever
	// the request context carries no TenancyContext override. See
	// issue #157.
	tenantOrgID       string
	tenantWorkspaceID string

	// Static entity stamp, installed once at agent startup via
	// WithEntity(). Populated from FORGE_AGENT_ID / cfg.AgentID
	// in the CLI runner with entityType hardcoded to "agent".
	// Every emit stamps these so SIEM consumers have a stable
	// (entity_id, entity_type) identity on each Forge NDJSON event.
	// See issue #164.
	tenantEntityID   string
	tenantEntityType string
}

// WithTenancy installs the deployment-time tenancy stamp on the
// AuditLogger. Both arguments are optional — passing "" disables
// the stamp for that field. Called once at runner startup after
// resolving FORGE_ORG_ID / FORGE_WORKSPACE_ID. Returns the receiver
// for fluent construction.
//
// Precedence at emit time (highest first):
//
//  1. Explicit OrgID/WorkspaceID set on the AuditEvent.
//  2. TenancyContext from the request context (per-request override
//     header X-Forge-Org-ID / X-Forge-Workspace-ID).
//  3. The static stamp installed here.
//
// Setting tenancy on an already-running AuditLogger is allowed but
// not the common path; hot-reload is the typical caller.
func (a *AuditLogger) WithTenancy(orgID, workspaceID string) *AuditLogger {
	a.mu.Lock()
	a.tenantOrgID = orgID
	a.tenantWorkspaceID = workspaceID
	a.mu.Unlock()
	return a
}

// tenancyStamp returns the static tenancy under lock so concurrent
// emit callers don't race against a hot-reload that re-runs
// WithTenancy. Internal — emit paths use this.
func (a *AuditLogger) tenancyStamp() (orgID, workspaceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tenantOrgID, a.tenantWorkspaceID
}

// WithEntity installs the deployment-time entity stamp on the
// AuditLogger. entityType matches the guardrails library's
// EntityType constants ("agent" / "workflow" / "assistant"); today
// Forge only runs agents, so the runner hardcodes "agent". Empty
// arguments disable the stamp for that field. Called once at runner
// startup after resolving FORGE_AGENT_ID / cfg.AgentID. Returns the
// receiver for fluent construction.
//
// Precedence at emit time (highest first):
//
//  1. Explicit EntityID/EntityType set on the AuditEvent.
//  2. The static stamp installed here.
//
// No per-request context layer: entity identity is fixed at process
// startup. If a deployment needs per-request entity routing, that's
// the tenancy layer's job (OrgID/WorkspaceID) — agent identity is
// the process, by definition.
//
// See issue #164.
func (a *AuditLogger) WithEntity(entityType, entityID string) *AuditLogger {
	a.mu.Lock()
	a.tenantEntityID = entityID
	a.tenantEntityType = entityType
	a.mu.Unlock()
	return a
}

// entityStamp returns the static entity under lock. Internal — emit
// paths use this.
func (a *AuditLogger) entityStamp() (entityID, entityType string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tenantEntityID, a.tenantEntityType
}

// NewAuditLogger creates a single-sink AuditLogger wrapping the given
// writer. Backward-compatible with pre-FWS-7 callers; tests and the
// CLI's per-command audit loggers (channel.go / run.go) continue to
// use this. Production code paths that need the export sink should use
// NewAuditLoggerFromConfig.
func NewAuditLogger(w io.Writer) *AuditLogger {
	name := "writer"
	if w == os.Stderr {
		name = "stderr"
	}
	return &AuditLogger{
		sinks:   []Sink{newWriterSink(w, name)},
		logOnce: map[string]bool{},
	}
}

// SetOpsLogger wires a structured logger into the audit pipeline for
// reporting sink failures (one log per (sink, error-class)). nil
// disables ops logging; in that mode sink errors are silently
// swallowed — appropriate for tests and for the channel CLI subcommand
// where there's no logger in scope.
func (a *AuditLogger) SetOpsLogger(l Logger) {
	a.mu.Lock()
	a.opsLog = l
	a.mu.Unlock()
}

// SetSigner installs an Ed25519 signer so every subsequent event is
// signed. Passing nil disables signing (subsequent events emit
// without Sig / Kid). Signing is opt-in — the runner calls this at
// startup when audit signing is configured; no configuration means
// pre-#213 wire shape. See docs/security/audit-signing.md.
func (a *AuditLogger) SetSigner(s *AuditSigner) {
	a.mu.Lock()
	a.signer = s
	a.mu.Unlock()
}

// AddSink appends a sink to the fan-out. Safe to call after
// construction (e.g. from a delayed sidecar discovery), but most
// callers should construct via NewAuditLoggerFromConfig.
func (a *AuditLogger) AddSink(s Sink) {
	a.mu.Lock()
	a.sinks = append(a.sinks, s)
	a.mu.Unlock()
}

// Sinks returns a snapshot of currently registered sinks. Used by the
// periodic audit_export_status emitter to read per-sink stats.
func (a *AuditLogger) Sinks() []Sink {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Sink, len(a.sinks))
	copy(out, a.sinks)
	return out
}

// Close drains every sink with the given deadline. Honors the context;
// sinks that don't drain in time are abandoned (each sink's Close is
// responsible for its own per-sink deadline derivation from ctx).
// Returns the first non-nil error from any sink so callers can surface
// shutdown problems; later errors are still logged via opsLog.
func (a *AuditLogger) Close(ctx context.Context) error {
	a.mu.Lock()
	sinks := append([]Sink(nil), a.sinks...)
	opsLog := a.opsLog
	a.mu.Unlock()

	var firstErr error
	for _, s := range sinks {
		if err := s.Close(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if opsLog != nil {
				opsLog.Warn("audit sink close failed", map[string]any{
					"sink":  s.Name(),
					"error": err.Error(),
				})
			}
		}
	}
	return firstErr
}

// Emit serializes an event and fans it out to every registered sink.
// Timestamp is populated to RFC3339 (UTC) if absent. Marshal failures
// are silently dropped — they indicate a programmer error (an
// AuditEvent with a non-serializable Fields value), and dropping
// matches the pre-FWS-7 behavior. Per-sink errors are logged once.
//
// Callers that have a request context.Context in scope should prefer
// EmitFromContext, which auto-tags CorrelationID, TaskID, and
// workflow-correlation fields.
func (a *AuditLogger) Emit(event AuditEvent) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	// Stamp the schema version on every event so consumers can detect
	// upgrades without parsing field-by-field. See issue #91 / FWS-8.
	if event.SchemaVersion == "" {
		event.SchemaVersion = AuditSchemaVersion
	}
	// Deployment-time tenancy stamp (#157). Plain Emit has no request
	// context, so the per-request header override path can't fire
	// here — but startup banners (agent_card_published, policy_loaded,
	// audit_export_status) are exactly the events that MUST carry the
	// deployment tenancy so SIEM filters work on every row, not just
	// per-invocation events.
	if event.OrgID == "" || event.WorkspaceID == "" {
		staticOrg, staticWS := a.tenancyStamp()
		if event.OrgID == "" {
			event.OrgID = staticOrg
		}
		if event.WorkspaceID == "" {
			event.WorkspaceID = staticWS
		}
	}
	// Deployment-time entity stamp (#164). Mirrors the tenancy stamp
	// but with no ctx layer since entity identity is process-fixed.
	if event.EntityID == "" || event.EntityType == "" {
		staticEntityID, staticEntityType := a.entityStamp()
		if event.EntityID == "" {
			event.EntityID = staticEntityID
		}
		if event.EntityType == "" {
			event.EntityType = staticEntityType
		}
	}
	// Governance R5 (#212, chain) + R6 (#213, signing) integration.
	//
	// Hold a.mu across the whole chain-mint → sign → marshal → hash →
	// sink-write sequence. Chain order MUST equal on-disk order —
	// two concurrent emits mustn't race on a.lastHash, and the sink
	// writes MUST land in the order they were chained (or a verifier
	// sees an event referencing a prev_hash whose predecessor hasn't
	// arrived).
	//
	// Deadlock avoidance: since logSinkErrorOnce reacquires a.mu
	// (sync.Mutex is non-reentrant), we CANNOT call it from inside
	// the locked section. Instead we collect sink errors under the
	// lock and log them after an explicit Unlock() below.
	type sinkErr struct {
		name string
		err  error
	}
	var (
		sinkErrs      []sinkErr
		signErr       error
		marshalErr    error
		droppedEvent  string
		opsLogForPost Logger
	)

	a.mu.Lock()

	// Chain stamp: pin this event to the previous event's line-hash.
	event.PrevHash = a.lastHash
	if event.PrevHash == "" {
		event.PrevHash = AuditChainGenesis
	}

	// Signing pass — signature covers every field except Sig itself
	// (canonicalBytesForSigning clones and blanks Sig). Because
	// PrevHash and Kid are stamped BEFORE canonicalize, tampering with
	// either breaks both the signature verification AND the chain
	// verification. See docs/security/audit-tamper-evidence.md.
	if a.signer != nil {
		event.Kid = a.signer.Kid()
		event.Sigp = SigCanonicalizationJCS1
		canonical, err := canonicalBytesForSigning(event)
		if err != nil {
			// Drop-and-log rather than silent drop or emit-unsigned:
			// on a signed pipeline the SIEM treats missing sig as
			// tampering evidence, so a visible gap (ops log + no
			// sink write + no chain update) is the right failure
			// mode. Chain state is preserved for the next call.
			signErr = err
			droppedEvent = event.Event
			opsLogForPost = a.opsLog
			a.mu.Unlock()
			if opsLogForPost != nil {
				opsLogForPost.Error("audit signing failed; event dropped", map[string]any{
					"event": droppedEvent,
					"error": signErr.Error(),
				})
			}
			return
		}
		event.Sig = a.signer.Sign(canonical)
	}

	// Marshal into the exact bytes we'll write AND hash.
	data, err := json.Marshal(event)
	if err != nil {
		marshalErr = err
		droppedEvent = event.Event
		opsLogForPost = a.opsLog
		a.mu.Unlock()
		if opsLogForPost != nil {
			opsLogForPost.Error("audit marshal failed; event dropped", map[string]any{
				"event": droppedEvent,
				"error": marshalErr.Error(),
			})
		}
		return
	}

	// Update chain state on the RAW line bytes (excluding the trailing
	// newline appended below). Hashing raw bytes — not the
	// re-marshaled event — is the fix for the precision hole reviewer
	// initializ-mk flagged: json.Marshal(json.Unmarshal(x)) is not a
	// fixed point when Fields carries integers > 2^53 (they round-trip
	// through float64). Hashing the producer-authored bytes sidesteps
	// the problem entirely.
	sum := sha256.Sum256(data)
	a.lastHash = hex.EncodeToString(sum[:])

	// Append newline for the NDJSON stream.
	data = append(data, '\n')

	// Sink writes, collecting errors for post-unlock logging.
	for _, s := range a.sinks {
		if err := s.Write(context.Background(), data); err != nil {
			sinkErrs = append(sinkErrs, sinkErr{name: s.Name(), err: err})
		}
	}
	opsLogForPost = a.opsLog

	a.mu.Unlock()

	// Deadlock-safe: log errors AFTER releasing a.mu so
	// logSinkErrorOnce can reacquire it.
	if opsLogForPost != nil {
		for _, se := range sinkErrs {
			a.logSinkErrorOnce(opsLogForPost, se.name, se.err)
		}
	}
}

// logSinkErrorOnce dedupes "sink is misbehaving" warnings to one line
// per sink lifetime. The first error from each sink hits the ops log;
// subsequent ones are suppressed. Stats counters carry the ongoing
// drop count for operators who want quantitative health.
func (a *AuditLogger) logSinkErrorOnce(opsLog Logger, sinkName string, err error) {
	if opsLog == nil {
		return
	}
	a.mu.Lock()
	already := a.logOnce[sinkName]
	if !already {
		a.logOnce[sinkName] = true
	}
	a.mu.Unlock()
	if already {
		return
	}
	opsLog.Warn("audit sink write failed (further errors suppressed)", map[string]any{
		"sink":  sinkName,
		"error": err.Error(),
	})
}

// EmitFromContext writes an audit event after auto-tagging
// CorrelationID, TaskID, and workflow-correlation fields from the
// request context. Fields already set on the passed event are
// preserved — the context is a fallback, not an override. This makes
// it safe to migrate callers from Emit to EmitFromContext: any
// already-explicit value continues to win.
func (a *AuditLogger) EmitFromContext(ctx context.Context, event AuditEvent) {
	if event.CorrelationID == "" {
		event.CorrelationID = CorrelationIDFromContext(ctx)
	}
	if event.TaskID == "" {
		event.TaskID = TaskIDFromContext(ctx)
	}
	if event.WorkflowID == "" || event.WorkflowExecutionID == "" || event.StageID == "" || event.StepID == "" || event.InvocationCaller == "" {
		wc := WorkflowContextFromContext(ctx)
		if event.WorkflowID == "" {
			event.WorkflowID = wc.WorkflowID
		}
		if event.WorkflowExecutionID == "" {
			event.WorkflowExecutionID = wc.WorkflowExecutionID
		}
		if event.StageID == "" {
			event.StageID = wc.StageID
		}
		if event.StepID == "" {
			event.StepID = wc.StepID
		}
		if event.InvocationCaller == "" {
			event.InvocationCaller = wc.InvocationCaller
		}
	}
	// Per-invocation sequence number: pulled from the counter the A2A
	// handler put on the context at request entry. NextSequence is
	// atomic + returns 0 when no counter is present (startup banner
	// events) — Sequence's omitempty tag drops the field in that case.
	// See issue #91 / FWS-8.
	if event.Sequence == 0 {
		event.Sequence = NextSequence(ctx)
	}
	// Phase 4 (#105) — stamp the active span's trace_id + span_id when
	// the context carries a recording span. SpanContext.IsValid() is
	// false for both "no span at all" (Background context) and "noop
	// span" (tracing disabled — the package-default tracer Phase 0
	// installed), so the omitempty tag drops both keys whenever
	// tracing isn't producing real spans. Net effect: when tracing is
	// off the audit JSON is byte-identical to pre-Phase-4 output.
	if event.TraceID == "" || event.SpanID == "" {
		sc := trace.SpanFromContext(ctx).SpanContext()
		if sc.IsValid() {
			if event.TraceID == "" {
				event.TraceID = sc.TraceID().String()
			}
			if event.SpanID == "" {
				event.SpanID = sc.SpanID().String()
			}
		}
	}
	// Tenancy stamp (#157) — per-request header override beats the
	// deployment-time stamp, which beats the omitempty default. Same
	// "context is fallback, not override" rule as the workflow keys
	// above, but we ALSO consult the AuditLogger's static stamp when
	// the ctx carries no override. Both fields are independent: the
	// caller can override one (e.g. WorkspaceID via header) and let
	// the other fall back to the env stamp.
	if event.OrgID == "" || event.WorkspaceID == "" {
		tc := TenancyContextFromContext(ctx)
		staticOrg, staticWS := a.tenancyStamp()
		if event.OrgID == "" {
			if tc.OrgID != "" {
				event.OrgID = tc.OrgID
			} else {
				event.OrgID = staticOrg
			}
		}
		if event.WorkspaceID == "" {
			if tc.WorkspaceID != "" {
				event.WorkspaceID = tc.WorkspaceID
			} else {
				event.WorkspaceID = staticWS
			}
		}
	}
	a.Emit(event)
}

// LLMCallAuditArgs is the shared input to AuditLogger.EmitLLMCall. The
// LLM call site captures these fields once at provider-call completion
// and the audit logger fans them out to the llm_call NDJSON event. The
// OTel tracing work (FORGE_OTEL_TRACING.md) will hook into this same
// capture point to populate gen_ai.usage.input_tokens /
// gen_ai.usage.output_tokens span attributes without re-doing the
// per-provider extraction. See issue #87 / FWS-3.
type LLMCallAuditArgs struct {
	Model     string
	Provider  string
	RequestID string
	Usage     LLMUsage
	Duration  time.Duration
	// Cancelled flips the emitted event from llm_call to llm_call_cancelled.
	// Used for streaming calls aborted mid-flight; partial usage counts are
	// still carried.
	Cancelled bool
	// Fields carries optional extra metadata to fold into the emitted
	// event's `fields` map. Populated by the runner's hook layer when
	// AuditPayloadCapture has any flag enabled (issue #91 / FWS-8):
	// captured prompt_messages, completion_text, etc. Nil for the
	// default metadata-only audit posture.
	Fields map[string]any
}

// LLMUsage carries the normalized token counts an LLM call site
// captures from provider response metadata. Mirrors llm.UsageInfo but
// kept in the runtime package so the audit layer has no llm-package
// dependency. The audit emitter sets TokensUnavailable=true on the
// event when both Input and Output are zero — signal to billing
// consumers that the provider did not report usage rather than
// "the call genuinely consumed zero tokens."
type LLMUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// EmitLLMCall builds and emits an llm_call (or llm_call_cancelled)
// audit event from the captured args. Routed through EmitFromContext
// so workflow-correlation fields (workflow_id / stage_id / step_id /
// invocation_caller from FWS-2) auto-tag every LLM call event when
// the inbound request carried orchestrator headers. This is the
// shared capture point that the OTel tracing work will hook into.
// See issue #87 / FWS-3.
func (a *AuditLogger) EmitLLMCall(ctx context.Context, args LLMCallAuditArgs) {
	evt := AuditEvent{
		Event:     AuditLLMCall,
		Model:     args.Model,
		Provider:  args.Provider,
		RequestID: args.RequestID,
	}
	if args.Cancelled {
		evt.Event = AuditLLMCallCancelled
	}
	in, out := args.Usage.InputTokens, args.Usage.OutputTokens
	evt.InputTokens = &in
	evt.OutputTokens = &out
	if in == 0 && out == 0 {
		evt.TokensUnavailable = true
	}
	d := args.Duration.Milliseconds()
	evt.DurationMs = &d
	if len(args.Fields) > 0 {
		evt.Fields = args.Fields
	}
	a.EmitFromContext(ctx, evt)
}

// EmitToolExec emits a tool_exec audit event tagged with the tool
// name + wall-clock duration. Routed through EmitFromContext so
// workflow-correlation fields auto-tag every tool execution when the
// inbound request was orchestrated. The Fields map may carry
// arg-shape metadata (e.g. arg sizes, types) — raw arg values are
// deliberately not emitted here; that question is FWS-8's
// payload-stripping concern, not FWS-3's. See issue #87 / FWS-3.
func (a *AuditLogger) EmitToolExec(ctx context.Context, tool string, duration time.Duration, fields map[string]any) {
	d := duration.Milliseconds()
	a.EmitFromContext(ctx, AuditEvent{
		Event:      AuditToolExec,
		DurationMs: &d,
		Fields:     mergeToolExecFields(tool, fields),
	})
}

func mergeToolExecFields(tool string, fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["tool"] = tool
	fields["phase"] = "end"
	return fields
}

// EmitInvocationComplete emits an invocation_complete audit event
// carrying the total wall-clock duration of the A2A invocation
// (auth → dispatch → engine.Execute → response). Routed through
// EmitFromContext so workflow-correlation fields are inherited from
// the inbound request. One event per invocation; emitted by the
// runner at the response boundary. See issue #87 / FWS-3.
func (a *AuditLogger) EmitInvocationComplete(ctx context.Context, duration time.Duration, fields map[string]any) {
	d := duration.Milliseconds()
	a.EmitFromContext(ctx, AuditEvent{
		Event:      AuditInvocationComplete,
		DurationMs: &d,
		Fields:     fields,
	})
}

// EmitInvocationCancelled emits an invocation_cancelled audit event
// for an in-flight A2A invocation that was signalled mid-execution
// via tasks/cancel (or internal cancellation: parent ctx deadline,
// graceful shutdown). Routed through EmitFromContext so workflow
// correlation auto-tags. The reason is folded into Fields["reason"]
// as a string — operators classify these via the CancellationReason
// constants but consumers should pass-through unknown values.
//
// Partial usage data should be present in fields (the runner reads
// the per-invocation LLMUsageAccumulator snapshot and adds
// input_tokens_total / output_tokens_total / llm_call_count / model
// / provider when llm_call_count > 0). When no LLM calls completed
// before cancellation, the field map carries reason + state only —
// downstream billing sees zero tokens which is correct: the
// invocation was cancelled before incurring spend.
//
// See issue #88 / FWS-4.
func (a *AuditLogger) EmitInvocationCancelled(ctx context.Context, reason CancellationReason, duration time.Duration, fields map[string]any) {
	d := duration.Milliseconds()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["reason"] = string(reason)
	a.EmitFromContext(ctx, AuditEvent{
		Event:      AuditInvocationCancelled,
		DurationMs: &d,
		Fields:     fields,
	})
}

// EmitPolicyLoaded emits a policy_loaded audit event at agent startup
// when a non-zero platform policy is active. Fields are a summary of
// the effective policy (deny-list sizes, max bounds, source path) —
// NOT the full policy contents, which can be large and may contain
// internal infrastructure hints operators don't want in every audit
// stream. Consumers that need the full policy can read the source
// file via the path field.
//
// Emitted via plain Emit (not EmitFromContext) because no request
// context exists at startup. See issue #89 / FWS-5.
func (a *AuditLogger) EmitPolicyLoaded(fields map[string]any) {
	a.Emit(AuditEvent{
		Event:  AuditPolicyLoaded,
		Fields: fields,
	})
}

// EmitPolicyViolationAtBuildTime emits a policy_violation_at_build_time
// audit event when forge.yaml's declaration conflicts with the
// platform policy. Fields carry the conflict detail (which kind of
// violation — denied_egress, denied_tool, forbidden_model, size_bound
// — and the offending value(s)). Called once at startup before the
// runner returns a non-zero exit; the audit lands even though the
// agent never serves traffic, so the operator's audit pipeline
// captures the violation.
//
// Emitted via plain Emit (not EmitFromContext) because no request
// context exists at startup. See issue #89 / FWS-5.
func (a *AuditLogger) EmitPolicyViolationAtBuildTime(fields map[string]any) {
	a.Emit(AuditEvent{
		Event:  AuditPolicyViolationAtBuildTime,
		Fields: fields,
	})
}

// EmitChannelDeniedByPolicy records that a channel adapter was skipped
// at startup because a policy layer's denied_channels list named it.
// `layer` identifies which file enforced ("system" / "user" /
// "workspace"); `source` is that file's path. Unlike egress/tool/model
// violations, channel deny does NOT abort startup — the agent runs
// without the denied channel. Operators see the skip in their audit
// pipeline and group by layer to understand which policy file owns
// the decision. See issue #90 / FWS-6.
func (a *AuditLogger) EmitChannelDeniedByPolicy(channel, layer, source string) {
	a.Emit(AuditEvent{
		Event: AuditChannelDeniedByPolicy,
		Fields: map[string]any{
			"channel": channel,
			"layer":   layer,
			"source":  source,
		},
	})
}

// Context key types for correlation IDs, task IDs, and file directories.
type correlationIDKey struct{}
type taskIDKey struct{}
type filesDirKey struct{}

// WithCorrelationID stores a correlation ID in the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationIDFromContext retrieves the correlation ID from the context.
// Returns "" if not set.
func CorrelationIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithTaskID stores a task ID in the context.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey{}, id)
}

// TaskIDFromContext retrieves the task ID from the context.
// Returns "" if not set.
func TaskIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(taskIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithFilesDir stores a files directory path in the context.
func WithFilesDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, filesDirKey{}, dir)
}

// FilesDirFromContext retrieves the files directory from the context.
// Returns "" if not set.
func FilesDirFromContext(ctx context.Context) string {
	if dir, ok := ctx.Value(filesDirKey{}).(string); ok {
		return dir
	}
	return ""
}

// GenerateID produces a 16-character hex random ID using crypto/rand.
func GenerateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback: return a fixed string (should never happen in practice)
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}
