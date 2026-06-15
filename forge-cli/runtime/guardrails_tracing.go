package runtime

import (
	"context"

	"github.com/initializ/guardrails"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Guardrail span instrumentation (issue #161).
//
// Symmetric to the guardrail_check audit-event emission shipped in
// #156 / #160 — every Check* method on LibraryGuardrailEngine opens
// a guardrail.<gate> span, stamps the same gate / decision /
// violation metadata the audit event carries, and (when
// CaptureContent is enabled) stamps the offending content as
// forge.guardrail.evidence via the same redact-then-truncate
// pipeline issue #130 established for OTel content capture.
//
// Span naming follows the audit-event gate vocabulary:
//
//   guardrail.input        (InputGate, CheckInbound)
//   guardrail.context      (ContextGate, CheckContext)
//   guardrail.tool_call    (ToolCallGate, CheckToolCall)
//   guardrail.output       (OutputGate, CheckOutbound + CheckToolOutput)
//   guardrail.stream       (StreamGate, CheckStream — not auto-wired)
//
// The span parent is whatever's active when the engine method is
// called (the A2A handler span for CheckInbound, agent.execute for
// the hook-driven gates). The noop tracer returned by Tracer() when
// tracing is disabled makes the SetAttributes / SetStatus / End
// calls near-zero cost — the engine calls them unconditionally.

// gateSpanName maps the engine's "what gate am I about to run" into
// the matching guardrail.<gate> span name. Used by startGuardrailSpan
// before the library call (we know which gate we're about to invoke;
// after the call we know it again via res.Gate, which acts as the
// single-source attribute value).
func gateSpanName(gate string) string {
	return "guardrail." + gate
}

// startGuardrailSpan opens a child span for one gate invocation. The
// span name is `guardrail.<gate>` where <gate> matches the library's
// Result.Gate value. Returns the child ctx + span; callers must call
// finishGuardrailSpan with the library Result (or nil if the call
// failed before producing one).
func startGuardrailSpan(ctx context.Context, gate, tool string) (context.Context, trace.Span) {
	ctx, span := coreruntime.Tracer().Start(ctx, gateSpanName(gate))
	if tool != "" {
		span.SetAttributes(attribute.String(observability.AttrForgeToolName, tool))
	}
	return ctx, span
}

// finishGuardrailSpan stamps the gate-result attributes on the span
// and closes it. Behavior matrix:
//
//   - res nil           → no attribute stamping (the gate call errored
//     before the library returned a Result); span ends as-is.
//   - DecisionBlock     → OTel Error status with the violation summary
//     as the status description. Surfaces blocked invocations as red
//     bars in the trace UI without custom attribute queries.
//   - DecisionMask/Warn → OK status (default).
//   - tracingCfg.CaptureContent → stamps
//     forge.guardrail.evidence with the post-mask content
//     (mask) or original content (warn/block), redact-then-truncated
//     per cfg.
//
// `content` is the value the caller wants stamped as evidence — the
// CheckInbound / CheckOutbound / CheckToolOutput sites pass the
// post-mask content for mask decisions and the original for
// warn/block decisions, matching the audit-event rule from PR #156.
func finishGuardrailSpan(
	span trace.Span,
	res *guardrails.Result,
	decisionString, content string,
	tracingCfg observability.TracingConfig,
) {
	defer span.End()
	if res == nil {
		return
	}
	span.SetAttributes(
		attribute.String(observability.AttrForgeGuardrailGate, string(res.Gate)),
		attribute.String(observability.AttrForgeGuardrailDecision, decisionString),
		attribute.Int(observability.AttrForgeGuardrailViolationCount, len(res.Violations)),
	)
	if len(res.Violations) > 0 {
		span.SetAttributes(attribute.String(observability.AttrForgeGuardrailType, res.Violations[0].Type))
		if cat := res.Violations[0].Category; cat != "" {
			span.SetAttributes(attribute.String(observability.AttrForgeGuardrailCategory, cat))
		}
	}
	if decisionString == guardrailResultBlocked {
		span.SetStatus(codes.Error, violationSummary(res))
	}
	if tracingCfg.CaptureContent && content != "" {
		ev := coreruntime.PrepareSpanContent(content, tracingCfg.Redact, coreruntime.DefaultSpanContentCapBytes)
		if ev != "" {
			span.SetAttributes(attribute.String(observability.AttrForgeGuardrailEvidence, ev))
		}
	}
}
