package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func installSpanRecorderScheduler(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() { coreruntime.SetTracerProvider(prev) })
	return rec
}

func findSpanByNameScheduler(t *testing.T, rec *tracetest.SpanRecorder, want string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range rec.Ended() {
		if s.Name() == want {
			return s
		}
	}
	t.Fatalf("no span named %q recorded; got %d spans", want, len(rec.Ended()))
	return nil
}

func attrValueScheduler(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

// TestScheduleFireSpan_StampsAttributesAndParentsDispatch is the
// core issue #187 invariant on the scheduler. fire() opens a span
// named "schedule.fire" whose attributes carry the schedule's id /
// cron / source — exactly the keys an operator filters on when
// asking "show me everything this scheduled job did over the past
// hour." The dispatch callback's ctx MUST carry that span so any
// downstream agent.execute / llm.completion / tool.<name> spans
// nest under it.
func TestScheduleFireSpan_StampsAttributesAndParentsDispatch(t *testing.T) {
	rec := installSpanRecorderScheduler(t)

	var dispatchParentTrace trace.TraceID
	var dispatchParentSpan trace.SpanID
	dispatch := func(ctx context.Context, sched Schedule) error {
		sc := trace.SpanContextFromContext(ctx)
		dispatchParentTrace = sc.TraceID()
		dispatchParentSpan = sc.SpanID()
		return nil
	}

	store := newMockStore()
	store.schedules["job-7"] = Schedule{
		ID:      "job-7",
		Cron:    "@hourly",
		Task:    "summarize-feed",
		Source:  "yaml",
		Enabled: true,
		Created: time.Now().UTC().Add(-time.Hour),
		LastRun: time.Now().UTC().Add(-time.Hour),
	}

	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)
	sched.tick(ctx)

	// Wait for the goroutine-backed fire() to finish — same pattern
	// as the existing scheduler_test.go tests.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.Ended()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	span := findSpanByNameScheduler(t, rec, "schedule.fire")
	if got := attrValueScheduler(span, observability.AttrForgeScheduleID); got != "job-7" {
		t.Errorf("schedule.id = %q, want job-7", got)
	}
	if got := attrValueScheduler(span, observability.AttrForgeScheduleCron); got != "@hourly" {
		t.Errorf("schedule.cron = %q, want @hourly", got)
	}
	if got := attrValueScheduler(span, observability.AttrForgeScheduleSource); got != "yaml" {
		t.Errorf("schedule.source = %q, want yaml", got)
	}
	// Dispatch ctx MUST be a child of schedule.fire.
	if dispatchParentTrace != span.SpanContext().TraceID() {
		t.Errorf("dispatch parent trace %s != schedule.fire trace %s",
			dispatchParentTrace, span.SpanContext().TraceID())
	}
	if dispatchParentSpan != span.SpanContext().SpanID() {
		t.Errorf("dispatch parent span %s != schedule.fire span %s",
			dispatchParentSpan, span.SpanContext().SpanID())
	}
}

// TestScheduleFireSpan_ErrorSetsStatusError pins the failure-path:
// the dispatch callback returning an error must surface as a span
// Status=Error so the error-rate dashboards reflect "fraction of
// fires that errored" without joining audit + trace streams.
func TestScheduleFireSpan_ErrorSetsStatusError(t *testing.T) {
	rec := installSpanRecorderScheduler(t)
	dispatch := func(_ context.Context, _ Schedule) error {
		return errors.New("simulated dispatch failure")
	}

	store := newMockStore()
	store.schedules["job-fail"] = Schedule{
		ID:      "job-fail",
		Cron:    "@hourly",
		Task:    "fail-me",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC().Add(-time.Hour),
		LastRun: time.Now().UTC().Add(-time.Hour),
	}
	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)
	sched.tick(ctx)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.Ended()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	span := findSpanByNameScheduler(t, rec, "schedule.fire")
	if code := span.Status().Code; code != codes.Error {
		t.Errorf("status code = %v, want Error", code)
	}
}

// TestScheduleFireSpan_SourceSurfacesLLMOriginatedSchedules is the
// observability question the issue calls out as the
// `schedule.source` attribute's reason for existing: operators want
// to distinguish "this fire came from forge.yaml" from "the agent's
// LLM created this schedule at runtime via schedule_create". Both
// are valid; lumping them together hides bugs.
func TestScheduleFireSpan_SourceSurfacesLLMOriginatedSchedules(t *testing.T) {
	rec := installSpanRecorderScheduler(t)
	var fired sync.Map
	dispatch := func(_ context.Context, sched Schedule) error {
		fired.Store(sched.ID, true)
		return nil
	}

	store := newMockStore()
	store.schedules["yaml-job"] = Schedule{
		ID: "yaml-job", Cron: "@hourly", Task: "y", Source: "yaml",
		Enabled: true,
		Created: time.Now().UTC().Add(-time.Hour),
		LastRun: time.Now().UTC().Add(-time.Hour),
	}
	store.schedules["llm-job"] = Schedule{
		ID: "llm-job", Cron: "@hourly", Task: "l", Source: "llm",
		Enabled: true,
		Created: time.Now().UTC().Add(-time.Hour),
		LastRun: time.Now().UTC().Add(-time.Hour),
	}
	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)
	sched.tick(ctx)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(rec.Ended()) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	sources := map[string]string{}
	for _, s := range rec.Ended() {
		if s.Name() != "schedule.fire" {
			continue
		}
		id := attrValueScheduler(s, observability.AttrForgeScheduleID)
		source := attrValueScheduler(s, observability.AttrForgeScheduleSource)
		sources[id] = source
	}
	if sources["yaml-job"] != "yaml" {
		t.Errorf("yaml-job source = %q, want yaml", sources["yaml-job"])
	}
	if sources["llm-job"] != "llm" {
		t.Errorf("llm-job source = %q, want llm", sources["llm-job"])
	}
}
