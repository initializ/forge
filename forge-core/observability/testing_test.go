package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

// TestNewTestTracerProvider_RecordsSpans confirms the
// SimpleSpanProcessor wiring: a span ended after the test installs the
// provider is visible via Spans() immediately, no ForceFlush required.
func TestNewTestTracerProvider_RecordsSpans(t *testing.T) {
	tp, rec := NewTestTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tr := tp.Tracer("test")
	_, span := tr.Start(context.Background(), "my-span")
	span.SetAttributes(attribute.String("k", "v"))
	span.End()

	got := rec.Spans()
	if len(got) != 1 {
		t.Fatalf("recorded %d spans; want 1", len(got))
	}
	if got[0].Name() != "my-span" {
		t.Errorf("Name() = %q; want %q", got[0].Name(), "my-span")
	}
	// Attribute set on the span survives to the exporter.
	var foundK bool
	for _, kv := range got[0].Attributes() {
		if string(kv.Key) == "k" && kv.Value.AsString() == "v" {
			foundK = true
		}
	}
	if !foundK {
		t.Error("attribute k=v not present on recorded span")
	}
}

func TestSpanRecorder_FindSpanAndFindSpans(t *testing.T) {
	tp, rec := NewTestTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	_, s := tr.Start(context.Background(), "alpha")
	s.End()
	_, s = tr.Start(context.Background(), "beta")
	s.End()
	_, s = tr.Start(context.Background(), "alpha")
	s.End()

	if _, ok := rec.FindSpan("alpha"); !ok {
		t.Error("FindSpan(alpha) should find the first alpha")
	}
	if _, ok := rec.FindSpan("does-not-exist"); ok {
		t.Error("FindSpan should miss for an unknown name")
	}
	if n := len(rec.FindSpans("alpha")); n != 2 {
		t.Errorf("FindSpans(alpha) returned %d; want 2", n)
	}
	if n := len(rec.FindSpans("beta")); n != 1 {
		t.Errorf("FindSpans(beta) returned %d; want 1", n)
	}
}

func TestSpanRecorder_ResetClearsHistory(t *testing.T) {
	tp, rec := NewTestTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("test")

	_, s := tr.Start(context.Background(), "one")
	s.End()
	if len(rec.Spans()) != 1 {
		t.Fatal("setup precondition failed")
	}
	rec.Reset()
	if len(rec.Spans()) != 0 {
		t.Errorf("Reset() did not clear; %d spans left", len(rec.Spans()))
	}
}

// TestWrapHTTPTransport_NilPassthrough — a nil base must return nil so
// the runner can wrap conditionally without guarding the call site.
func TestWrapHTTPTransport_NilPassthrough(t *testing.T) {
	if WrapHTTPTransport(nil) != nil {
		t.Error("WrapHTTPTransport(nil) must return nil for caller-guard-free wiring")
	}
}

// TestWrapHTTPTransport_RoundTripsRequestAndCallsBase — minimal proof
// the wrapper actually delegates to the wrapped transport. A more
// thorough check (HTTP-client span name, attributes) belongs in
// instrumentation-site tests that use the recorder.
func TestWrapHTTPTransport_RoundTripsRequestAndCallsBase(t *testing.T) {
	tp, _ := NewTestTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := &http.Client{Transport: WrapHTTPTransport(http.DefaultTransport)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 1 {
		t.Errorf("wrapped transport did not delegate to the base transport (hits=%d)", hits.Load())
	}
}
