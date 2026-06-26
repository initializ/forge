package channels

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// StartDeliverSpan opens a `channel.<adapter>.deliver` span around an
// inbound channel adapter's per-message handler and returns a finish
// closure that records the error and ends the span. The span attributes
// mirror the upstream-system metadata an operator needs to pivot from
// the trace back to the Slack thread / Telegram chat / Teams message
// that produced this invocation. Issue #187.
//
// The span PARENTS the internal A2A POST that
// `forge-cli/channels/router.go` issues, because that router injects
// the W3C traceparent from the calling ctx onto the outbound HTTP
// request — the A2A server's `a2a.tasks/send` span then nests under
// this delivery span instead of starting as an orphan root.
//
// Usage shape (defer captures the named return so finish sees the
// final err value):
//
//	go func() {
//	    ctx, _, finish := channels.StartDeliverSpan(ctx, "slack", event)
//	    var err error
//	    defer finish(&err)
//	    // ... handler work mutates err ...
//	}()
//
// When tracing is disabled the global tracer is a no-op and Start
// returns an empty span with zero allocations — safe to call on
// every inbound message regardless of tracing posture.
func StartDeliverSpan(ctx context.Context, adapter string, event *ChannelEvent) (context.Context, trace.Span, func(*error)) {
	ctx, span := coreruntime.Tracer().Start(ctx, "channel."+adapter+".deliver")
	attrs := []attribute.KeyValue{
		attribute.String(observability.AttrForgeChannelAdapter, adapter),
	}
	if event != nil {
		if event.WorkspaceID != "" {
			// WorkspaceID is the per-adapter "conversational location"
			// identifier — Slack channel ID, Telegram chat ID, Teams
			// chat ID. Stamped as channel.target so SIEM filters pivot
			// to the upstream system without translating per adapter.
			attrs = append(attrs, attribute.String(
				observability.AttrForgeChannelTarget, event.WorkspaceID))
		}
		if event.MessageID != "" {
			attrs = append(attrs, attribute.String(
				observability.AttrForgeChannelMessageID, event.MessageID))
		}
		if event.UserID != "" {
			attrs = append(attrs, attribute.String(
				observability.AttrForgeChannelUserID, event.UserID))
		}
	}
	span.SetAttributes(attrs...)

	finish := func(errPtr *error) {
		if errPtr != nil && *errPtr != nil {
			span.SetStatus(codes.Error, (*errPtr).Error())
		}
		span.End()
	}
	return ctx, span, finish
}
