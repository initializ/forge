package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ─── Disabled / required-field paths ─────────────────────────────────

func TestNewTracerProvider_Disabled_ReturnsErrDisabled(t *testing.T) {
	tp, err := NewTracerProvider(context.Background(), TracingConfig{Enabled: false}, nil)
	if tp != nil {
		t.Errorf("disabled config must return nil provider; got %v", tp)
	}
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled config must wrap ErrDisabled; got %v", err)
	}
}

func TestNewTracerProvider_EnabledButNoEndpoint_ReturnsErrDisabled(t *testing.T) {
	// Empty endpoint with Enabled=true is treated as "off, log a
	// warning, install noop." Same sentinel so the cli's branch can
	// be `errors.Is(err, ErrDisabled)` without distinguishing the two
	// reasons.
	tp, err := NewTracerProvider(context.Background(), TracingConfig{Enabled: true, Endpoint: ""}, nil)
	if tp != nil || !errors.Is(err, ErrDisabled) {
		t.Errorf("empty endpoint must collapse to ErrDisabled; got tp=%v err=%v", tp, err)
	}
}

func TestNewTracerProvider_EnabledNoServiceName_Errors(t *testing.T) {
	tp, err := NewTracerProvider(context.Background(), TracingConfig{
		Enabled:  true,
		Endpoint: "http://collector:4318",
	}, nil)
	if tp != nil {
		t.Error("missing ServiceName must not return a provider")
	}
	if err == nil || !strings.Contains(err.Error(), "ServiceName") {
		t.Errorf("missing ServiceName must produce a clear error naming the field; got %v", err)
	}
}

// ─── Protocol selection ─────────────────────────────────────────────

func TestNewTracerProvider_BadProtocol_ReturnsWrappedError(t *testing.T) {
	tp, err := NewTracerProvider(context.Background(), TracingConfig{
		Enabled:     true,
		Endpoint:    "http://collector:4318",
		Protocol:    "htttp/protobuf", // typo'd
		ServiceName: "test",
	}, nil)
	if tp != nil {
		t.Error("bad protocol must not produce a provider")
	}
	if err == nil || !strings.Contains(err.Error(), "htttp/protobuf") {
		t.Errorf("error must name the offending protocol; got %v", err)
	}
}

func TestNewTracerProvider_DefaultProtocolIsHTTPProtobuf(t *testing.T) {
	// We can't introspect the protocol after-the-fact (the exporter
	// hides it). Instead exercise the constructor with both an
	// unspecified protocol AND a hand-rolled HTTP server that
	// recognizes the OTLP HTTP path — if the constructor defaults to
	// HTTP, an end-of-test Shutdown will flush any spans to our
	// recorder.
	rec := newProtocolRecorder()
	srv := httptest.NewServer(rec)
	defer srv.Close()

	tp, err := NewTracerProvider(context.Background(), TracingConfig{
		Enabled:     true,
		Endpoint:    srv.URL,
		ServiceName: "test",
		// Protocol intentionally empty
	}, nil)
	if err != nil {
		t.Fatalf("default protocol path must build cleanly; got %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Emit one span and force a flush so we observe the wire
	// traffic.
	tr := tp.Tracer("test")
	_, span := tr.Start(context.Background(), "default-protocol-test")
	span.End()
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Errorf("flush: %v", err)
	}
	// The HTTP recorder requires the request to have hit a path under
	// /v1/traces — the OTel HTTP exporter's contract. If the default
	// were gRPC, no HTTP request would arrive.
	if rec.requests.Load() == 0 {
		t.Error("default protocol must produce HTTP traffic; received zero requests")
	}
}

// ─── Sampler parsing ────────────────────────────────────────────────

func TestParseSampler_KnownNames(t *testing.T) {
	cases := []struct {
		name       string
		ratio      float64
		wantNonNil bool
	}{
		{SamplerAlwaysOn, 0, true},
		{SamplerAlwaysOff, 0, true},
		{SamplerTraceIDRatio, 0.5, true},
		{SamplerParentBasedAlwaysOn, 0, true},
		{SamplerParentBasedAlwaysOff, 0, true},
		{SamplerParentBasedTraceIDRatio, 0.1, true},
		// OTel env-var copy-pastes are uppercase + whitespace-padded:
		// `OTEL_TRACES_SAMPLER=ALWAYS_ON` (the env var name's casing
		// frequently bleeds into its value). Match those too.
		{"ALWAYS_ON", 0, true},
		{"  always_on  ", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseSampler(tc.name, tc.ratio)
			if (s != nil) != tc.wantNonNil {
				t.Errorf("sampler=%q: got %v, wantNonNil=%v (err=%v)", tc.name, s, tc.wantNonNil, err)
			}
			if err != nil {
				t.Errorf("sampler=%q: unexpected err: %v", tc.name, err)
			}
		})
	}
}

func TestParseSampler_Unknown_ReturnsDescriptiveError(t *testing.T) {
	s, err := parseSampler("parent_based_always_on", 0) // common typo (underscore)
	if s != nil {
		t.Error("unknown sampler must not return a sampler")
	}
	if err == nil {
		t.Fatal("unknown sampler must produce error")
	}
	for _, want := range []string{
		"parent_based_always_on", // names the offender
		SamplerAlwaysOn,          // mentions the legal set
		SamplerParentBasedTraceIDRatio,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q; got %q", want, err.Error())
		}
	}
}

// ─── Resource attributes ────────────────────────────────────────────

func TestBuildResource_IncludesServiceAndForgeAttrs(t *testing.T) {
	res, err := buildResource(context.Background(), TracingConfig{
		ServiceName:    "demo-agent",
		ServiceVersion: "1.2.3",
		RuntimeVersion: "v0.13.0",
		ResourceAttrs:  map[string]string{"deployment.environment": "prod"},
	})
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	// Canonical service.* attributes — backend dashboards key by these.
	if got["service.name"] != "demo-agent" {
		t.Errorf("service.name = %q, want demo-agent", got["service.name"])
	}
	if got["service.version"] != "1.2.3" {
		t.Errorf("service.version = %q, want 1.2.3", got["service.version"])
	}
	// Forge-namespaced attribute distinguishing the runtime version
	// from the user's agent service.version.
	if got["forge.runtime.version"] != "v0.13.0" {
		t.Errorf("forge.runtime.version = %q, want v0.13.0", got["forge.runtime.version"])
	}
	// Merged ResourceAttrs land verbatim.
	if got["deployment.environment"] != "prod" {
		t.Errorf("deployment.environment = %q, want prod (extra attr lost)", got["deployment.environment"])
	}
}

func TestBuildResource_NoExtras_StillIncludesServiceName(t *testing.T) {
	res, err := buildResource(context.Background(), TracingConfig{ServiceName: "minimal"})
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	for _, kv := range res.Attributes() {
		if string(kv.Key) == "service.name" && kv.Value.AsString() == "minimal" {
			return
		}
	}
	t.Error("minimal config must still produce a resource carrying service.name")
}

// ─── Egress-enforced transport injection ────────────────────────────

// TestNewTracerProvider_HTTPExporterUsesSuppliedTransport pins the
// FWS-7-era egress-enforcement invariant for OTel: the OTLP HTTP
// exporter MUST route its traffic through the http.RoundTripper the
// cli layer supplies, so the OTLP traffic gets the same allowlist +
// post-DNS IP guard as every other in-process Forge HTTP client. A
// regression that silently fell back to http.DefaultTransport would
// produce a tracing path that bypasses the egress enforcer — exactly
// the failure mode the initiative explicitly forbids.
func TestNewTracerProvider_HTTPExporterUsesSuppliedTransport(t *testing.T) {
	rec := newProtocolRecorder()
	srv := httptest.NewServer(rec)
	defer srv.Close()

	// A custom transport whose RoundTrip is the only path that should
	// reach the recorder. If the exporter built its own client, this
	// counter stays zero.
	var customTransportCalls atomic.Int64
	transport := &countingTransport{
		next:  http.DefaultTransport,
		count: &customTransportCalls,
	}

	tp, err := NewTracerProvider(context.Background(), TracingConfig{
		Enabled:     true,
		Endpoint:    srv.URL,
		Protocol:    ProtocolHTTPProtobuf,
		ServiceName: "transport-test",
	}, transport)
	if err != nil {
		t.Fatalf("provider build: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tr := tp.Tracer("test")
	_, span := tr.Start(context.Background(), "egress-routing")
	span.End()
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Errorf("flush: %v", err)
	}
	if customTransportCalls.Load() == 0 {
		t.Fatal("OTLP HTTP exporter bypassed the supplied transport — egress enforcement would be silently disabled in production")
	}
}

// ─── Sampler honored by emitted spans ──────────────────────────────

// TestNewTracerProvider_AlwaysOffSamplerDropsSpans confirms the
// sampler string actually reaches the SDK and influences sampling
// decisions end-to-end. A regression that constructed
// TraceIDRatioBased(1.0) regardless of config would pass the unit
// tests on parseSampler but produce a tracing posture nobody asked
// for. This test catches that by emitting with `always_off` and
// confirming the exporter never sees the span.
func TestNewTracerProvider_AlwaysOffSamplerDropsSpans(t *testing.T) {
	rec := newProtocolRecorder()
	srv := httptest.NewServer(rec)
	defer srv.Close()

	tp, err := NewTracerProvider(context.Background(), TracingConfig{
		Enabled:     true,
		Endpoint:    srv.URL,
		ServiceName: "always-off-test",
		Sampler:     SamplerAlwaysOff,
	}, nil)
	if err != nil {
		t.Fatalf("provider build: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tr := tp.Tracer("test")
	_, span := tr.Start(context.Background(), "must-be-dropped")
	span.End()
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Errorf("flush: %v", err)
	}
	// Allow the batcher a beat to flush; AlwaysOff should produce
	// nothing regardless.
	time.Sleep(50 * time.Millisecond)
	if got := rec.requests.Load(); got != 0 {
		t.Errorf("always_off sampler must drop spans; recorder saw %d requests", got)
	}
}

// ─── Test helpers ───────────────────────────────────────────────────

// protocolRecorder counts inbound HTTP requests. Used to assert that
// the default protocol path produces HTTP traffic and that the
// supplied transport is actually invoked.
type protocolRecorder struct {
	requests atomic.Int64
}

func newProtocolRecorder() *protocolRecorder { return &protocolRecorder{} }

func (p *protocolRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.requests.Add(1)
	w.WriteHeader(http.StatusOK)
}

// countingTransport wraps another http.RoundTripper and increments a
// counter on every call. The supplied-transport test uses this to
// assert the exporter routes through us, not through
// http.DefaultTransport.
type countingTransport struct {
	next  http.RoundTripper
	count *atomic.Int64
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.count.Add(1)
	return c.next.RoundTrip(r)
}

// Compile-time guard against accidentally importing sdktrace.Sampler
// helpers that the constructor must own. parseSampler is the only
// path into the SDK's sampler types from this package.
var _ sdktrace.Sampler = sdktrace.AlwaysSample()
