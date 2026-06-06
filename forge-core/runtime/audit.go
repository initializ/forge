package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"time"
)

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

	// Deprecated: use EventAuthVerify. Kept as a string alias so any
	// audit-log consumer that grep'd for "auth_success" can be migrated.
	// Scheduled for removal in v0.11.0.
	AuditAuthSuccess = EventAuthVerify
	// Deprecated: use EventAuthFail. Same migration window as AuditAuthSuccess.
	AuditAuthFailure = EventAuthFail
)

// AuditEvent is a single structured audit record emitted as NDJSON.
//
// Workflow correlation fields (WorkflowID, StageID, StepID,
// InvocationCaller) are tagged onto every event emitted via
// EmitFromContext when the request carries `X-Workflow-*` /
// `X-Invocation-Caller` headers from any A2A-compatible orchestrator.
// Direct A2A invocations omit them entirely so the JSON shape matches
// the pre-FWS-2 audit consumers.
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

	// CorrelationID groups events from a single agent invocation —
	// generated by the A2A handler at request entry.
	CorrelationID string `json:"correlation_id,omitempty"`

	// TaskID is the A2A task identifier (params.id on tasks/send).
	TaskID string `json:"task_id,omitempty"`

	// WorkflowID identifies the orchestrator-level workflow run that
	// invoked this agent. Sourced from X-Workflow-ID at request entry;
	// absent for direct A2A invocations.
	WorkflowID string `json:"workflow_id,omitempty"`

	// StageID identifies the workflow stage that invoked this agent.
	StageID string `json:"stage_id,omitempty"`

	// StepID identifies the workflow step that invoked this agent.
	StepID string `json:"step_id,omitempty"`

	// InvocationCaller identifies the upstream caller (orchestrator
	// or upstream agent in an agent-to-agent flow).
	InvocationCaller string `json:"invocation_caller,omitempty"`

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

	Fields map[string]any `json:"fields,omitempty"`
}

// AuditLogger writes structured NDJSON audit events to an io.Writer.
type AuditLogger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewAuditLogger creates a new AuditLogger writing to w.
func NewAuditLogger(w io.Writer) *AuditLogger {
	return &AuditLogger{w: w}
}

// Emit writes an audit event as a single NDJSON line. If Timestamp is empty
// it is set to the current time in RFC3339 format.
//
// Callers that have a request context.Context in scope should prefer
// EmitFromContext, which automatically tags CorrelationID, TaskID, and
// the workflow-correlation fields (WorkflowID, StageID, StepID,
// InvocationCaller) from the context. Emit is kept for the few sites
// that emit outside a request scope (e.g. agent_card_published at
// startup).
func (a *AuditLogger) Emit(event AuditEvent) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')

	a.mu.Lock()
	a.w.Write(data) //nolint:errcheck
	a.mu.Unlock()
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
	if event.WorkflowID == "" || event.StageID == "" || event.StepID == "" || event.InvocationCaller == "" {
		wc := WorkflowContextFromContext(ctx)
		if event.WorkflowID == "" {
			event.WorkflowID = wc.WorkflowID
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
