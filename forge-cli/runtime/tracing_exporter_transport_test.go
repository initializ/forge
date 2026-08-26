package runtime

import (
	"net/http"
	"testing"

	"github.com/initializ/forge/forge-core/security"
)

// TestNewTracingExporterTransport_Unaudited is the regression guard for #424:
// the OTLP trace exporter must be handed an egress-enforced but UNAUDITED,
// non-otel-wrapped transport. If a refactor re-wires the exporter onto the
// audited egress client, tracing re-floods the audit stream with one
// egress_allowed per ~5s batch export (and the exporter self-traces its own
// POST). This pins the two properties that break that loop while proving the
// security posture is preserved.
func TestNewTracingExporterTransport_Unaudited(t *testing.T) {
	tr := newTracingExporterTransport(security.ModeAllowlist, []string{"collector.example.com"}, false, nil)
	if tr == nil {
		t.Fatal("tracing exporter transport must not be nil for a resolved egress config")
	}

	// (1) No audit hook. The audited egress client sets OnAttempt (which emits
	// egress_allowed); the exporter transport must never carry one.
	if tr.OnAttempt != nil {
		t.Error("tracing exporter transport must NOT set OnAttempt — it would re-emit egress_allowed on every export (#424)")
	}

	// (2) Raw enforcer, not otelhttp-wrapped. The concrete *EgressEnforcer type
	// is the compile-time guarantee (observability.WrapHTTPTransport returns a
	// different concrete type); this documents that a plain RoundTripper is what
	// the exporter receives, so its POST never self-traces.
	var _ http.RoundTripper = tr

	// (3) Allowlist + IP guard preserved. A non-allowlisted host is rejected
	// pre-dial (no network I/O), proving egress enforcement still applies so a
	// misconfigured collector can't exfiltrate span content.
	req, err := http.NewRequest(http.MethodPost, "https://not-allowed.example.net/v1/traces", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if _, err := tr.RoundTrip(req); err == nil {
		t.Error("tracing exporter transport must still enforce the allowlist — a non-allowlisted host should be blocked")
	}

	// And an allowlisted host is NOT rejected by the matcher (it proceeds to the
	// dialer; we don't assert the dial itself to avoid network I/O).
	allowed := security.NewDomainMatcher(security.ModeAllowlist, []string{"collector.example.com"})
	if !allowed.IsAllowed("collector.example.com") {
		t.Error("sanity: the allowlisted collector host should match the allowlist")
	}
}
