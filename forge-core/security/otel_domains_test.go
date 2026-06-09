package security_test

import (
	"reflect"
	"testing"

	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/types"
)

// TestOTelDomain_Empty pins the "no telemetry → no allowlist entry"
// invariant: a forge.yaml with no observability.tracing block produces
// no additions to the egress allowlist. Without this, an empty config
// could spuriously inject "" (the empty hostname) and break matcher
// resolution downstream.
func TestOTelDomain_Empty(t *testing.T) {
	if got := security.OTelDomain(types.TracingYAML{}); got != nil {
		t.Errorf("OTelDomain(empty) = %v, want nil", got)
	}
}

// TestOTelDomain_DisabledIgnoresEndpoint guards the "off by default"
// posture the initiative ruled on (#108): even when an endpoint is
// configured, Enabled=false means tracing is dormant and the egress
// allowlist must not carry the collector host. Otherwise turning
// tracing off in yaml would leave a stale entry punched through the
// NetworkPolicy.
func TestOTelDomain_DisabledIgnoresEndpoint(t *testing.T) {
	got := security.OTelDomain(types.TracingYAML{
		Enabled:  false,
		Endpoint: "https://otel.example.com:4318/v1/traces",
	})
	if got != nil {
		t.Errorf("disabled tracing must not contribute an allowlist entry; got %v", got)
	}
}

// TestOTelDomain_HTTPCollector covers the recommended HTTP/Protobuf
// path. The endpoint URL includes scheme, port, and a path; only the
// hostname should survive into the allowlist. Phase 6's contract
// matches the AuthDomains / MCPDomains convention — hostname only, no
// port (the egress matcher is port-agnostic).
func TestOTelDomain_HTTPCollector(t *testing.T) {
	got := security.OTelDomain(types.TracingYAML{
		Enabled:  true,
		Endpoint: "https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces",
	})
	want := []string{"otel-collector.monitoring.svc.cluster.local"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OTelDomain = %v, want %v", got, want)
	}
}

// TestOTelDomain_GRPCCollector covers the OTLP/gRPC path. Forge's
// tracing config accepts `protocol: grpc` with a host:port endpoint
// (no scheme, no path); the hostFromURL helper handles this via
// url.Parse's host parsing rules. The Phase-1 PR explicitly
// recommends HTTP because gRPC bypasses the in-process egress
// enforcer — but the build-time allowlist contribution still needs to
// land so the NetworkPolicy admits the traffic.
func TestOTelDomain_GRPCCollector(t *testing.T) {
	got := security.OTelDomain(types.TracingYAML{
		Enabled:  true,
		Protocol: "grpc",
		// gRPC endpoints carry a scheme too (the SDK normalizes either
		// host:port OR a scheme://host:port form). We test the
		// scheme-bearing form because url.Parse would otherwise
		// interpret the bare host:port as a relative path.
		Endpoint: "https://otel-collector.monitoring.svc.cluster.local:4317",
	})
	want := []string{"otel-collector.monitoring.svc.cluster.local"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OTelDomain (grpc) = %v, want %v", got, want)
	}
}

// TestOTelDomain_MalformedEndpoint_NoEntry confirms the
// fault-tolerance contract: a malformed endpoint URL must NOT cause
// the build to fail. The cli's tracing config resolver (Phase 2) is
// the single place that fails loudly on bad config; the build stage
// silently skips and lets the runtime path surface the error so a
// broken telemetry URL never blocks an otherwise-valid deployment.
func TestOTelDomain_MalformedEndpoint_NoEntry(t *testing.T) {
	got := security.OTelDomain(types.TracingYAML{
		Enabled:  true,
		Endpoint: "::::not-a-url::::",
	})
	if got != nil {
		t.Errorf("malformed endpoint must produce no entry; got %v", got)
	}
}

// TestOTelDomain_EmptyEndpoint_NoEntry — Enabled=true but no Endpoint
// is the "tracing is wired but unconfigured" state. The cli resolver
// already returns ErrDisabled here at runtime; the build stage
// matches that posture — no allowlist entry, no error.
func TestOTelDomain_EmptyEndpoint_NoEntry(t *testing.T) {
	got := security.OTelDomain(types.TracingYAML{
		Enabled:  true,
		Endpoint: "",
	})
	if got != nil {
		t.Errorf("empty endpoint must produce no entry; got %v", got)
	}
}
