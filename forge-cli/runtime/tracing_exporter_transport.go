package runtime

import (
	"net"

	"github.com/initializ/forge/forge-core/security"
)

// newTracingExporterTransport builds the egress-enforced RoundTripper handed to
// the OTLP trace exporter (issue #424). It is deliberately DISTINCT from the
// audited egress client the rest of the runtime uses:
//
//   - It sets NO OnAttempt hook. The audited client's OnAttempt emits an
//     egress_allowed audit event on every request; because the batch exporter
//     exports on a ~5s timer, reusing that path floods the audit stream with one
//     egress_allowed per 5s — forever, even on an idle agent.
//   - The concrete *security.EgressEnforcer return type (not http.RoundTripper)
//     is a compile-time guard that this transport is never otelhttp-wrapped:
//     observability.WrapHTTPTransport returns a different concrete type, so
//     wrapping it here would not compile. Wrapping would make the exporter POST
//     self-trace, enqueuing a span that forces the next export — the
//     self-sustaining loop behind #424.
//
// It keeps the SAME allowlist + post-DNS IP guard as the audited client
// (base=nil → SafeTransport), so a misconfigured collector host still cannot
// exfiltrate span content. The caller must not set OnAttempt on the result.
//
// See TestNewTracingExporterTransport_Unaudited for the regression guard.
func newTracingExporterTransport(mode security.EgressMode, domains []string, allowPrivateIPs bool, allowedPrivateCIDRs []*net.IPNet) *security.EgressEnforcer {
	return security.NewEgressEnforcer(nil, mode, domains, allowPrivateIPs, allowedPrivateCIDRs)
}
