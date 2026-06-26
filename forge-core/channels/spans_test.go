package channels

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// installSpanRecorder swaps the global tracer provider for one that
// records every emitted span and restores the prior provider on
// cleanup. Mirrors the helper in forge-core/auth/auth_span_test.go.
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() { coreruntime.SetTracerProvider(prev) })
	return rec
}

func findSpanByName(t *testing.T, rec *tracetest.SpanRecorder, want string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range rec.Ended() {
		if s.Name() == want {
			return s
		}
	}
	t.Fatalf("no span named %q recorded; got %d spans", want, len(rec.Ended()))
	return nil
}

func attrValue(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

// TestStartDeliverSpan_StampsAdapterAndEventAttributes pins the issue
// #187 channel-attribute contract. The span's adapter / target /
// message_id / user_id attributes are sourced from the ChannelEvent
// and the adapter name passed by the caller. Operators pivoting from
// a customer support ticket ("find the trace for message X in
// channel Y") rely on these being stamped consistently across all
// three adapters.
func TestStartDeliverSpan_StampsAdapterAndEventAttributes(t *testing.T) {
	rec := installSpanRecorder(t)
	event := &ChannelEvent{
		Channel:     "slack",
		WorkspaceID: "C0123",
		ThreadID:    "1700000000.000001",
		UserID:      "U987",
		MessageID:   "1700000000.000200",
	}
	_, _, finish := StartDeliverSpan(context.Background(), "slack", event)
	var err error
	finish(&err)

	s := findSpanByName(t, rec, "channel.slack.deliver")
	if got := attrValue(s, observability.AttrForgeChannelAdapter); got != "slack" {
		t.Errorf("adapter = %q, want slack", got)
	}
	if got := attrValue(s, observability.AttrForgeChannelTarget); got != "C0123" {
		t.Errorf("target = %q, want C0123", got)
	}
	if got := attrValue(s, observability.AttrForgeChannelMessageID); got != "1700000000.000200" {
		t.Errorf("message_id = %q", got)
	}
	if got := attrValue(s, observability.AttrForgeChannelUserID); got != "U987" {
		t.Errorf("user_id = %q, want U987", got)
	}
}

// TestStartDeliverSpan_ErrorSetsStatus is the failure-path contract.
// Adapter handlers that return an error set Status=Error so the
// error-rate dashboards work uniformly across the auth.verify /
// channel.<adapter>.deliver / schedule.fire span families.
func TestStartDeliverSpan_ErrorSetsStatus(t *testing.T) {
	rec := installSpanRecorder(t)
	_, _, finish := StartDeliverSpan(context.Background(), "telegram", &ChannelEvent{WorkspaceID: "chat-1"})
	err := errors.New("simulated handler failure")
	finish(&err)

	s := findSpanByName(t, rec, "channel.telegram.deliver")
	if code := s.Status().Code; code != codes.Error {
		t.Errorf("status code = %v, want Error", code)
	}
	if desc := s.Status().Description; desc != "simulated handler failure" {
		t.Errorf("status desc = %q", desc)
	}
}

// TestStartDeliverSpan_AdapterNameDrivesSpanName pins the
// `channel.<adapter>.deliver` naming convention. Switching adapter
// changes the span name. Three runtimes' worth of trace dashboards
// pre-filter on the literal span name to build "Slack→agent
// latency" tiles; renaming the convention breaks them.
func TestStartDeliverSpan_AdapterNameDrivesSpanName(t *testing.T) {
	rec := installSpanRecorder(t)
	for _, adapter := range []string{"slack", "telegram", "msteams"} {
		_, _, finish := StartDeliverSpan(context.Background(), adapter, &ChannelEvent{WorkspaceID: "x"})
		var err error
		finish(&err)
		findSpanByName(t, rec, "channel."+adapter+".deliver")
	}
}

// TestStartDeliverSpan_ChildContextCarriesActiveSpan is the parent-
// child contract that drives `traceparent` injection in the router.
// The downstream A2A POST runs under the returned ctx; when its
// outbound request gets the W3C traceparent header injected, the
// downstream a2a.tasks/send span nests under
// channel.<adapter>.deliver. If the returned ctx didn't carry the
// span, the injection writes a no-op header and the nesting silently
// breaks.
func TestStartDeliverSpan_ChildContextCarriesActiveSpan(t *testing.T) {
	installSpanRecorder(t)
	ctx, span, finish := StartDeliverSpan(context.Background(), "slack", &ChannelEvent{WorkspaceID: "x"})
	var err error
	defer finish(&err)

	got := trace.SpanFromContext(ctx)
	if got.SpanContext().SpanID() != span.SpanContext().SpanID() {
		t.Errorf("ctx-derived span %s != returned span %s",
			got.SpanContext().SpanID(), span.SpanContext().SpanID())
	}
}

// TestStartDeliverSpan_NilEventDoesNotCrash confirms the guard works
// for the empty-event path that occurs in tests / on the
// unconfigured-channels code path.
func TestStartDeliverSpan_NilEventDoesNotCrash(t *testing.T) {
	installSpanRecorder(t)
	_, _, finish := StartDeliverSpan(context.Background(), "slack", nil)
	var err error
	finish(&err) // must not panic
}
