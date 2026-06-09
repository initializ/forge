package security

import "github.com/initializ/forge/forge-core/types"

// OTelDomain returns the hostname of the OTLP collector configured in
// observability.tracing.endpoint, as a single-element slice ready to
// be merged into the egress allowlist alongside AuthDomains and
// MCPDomains.
//
// Why this exists (Phase 6 of OTel Tracing v1, #107 / #108):
//
//	Without this entry, a deployment with tracing enabled in
//	forge.yaml ships a NetworkPolicy that blocks the OTLP exporter's
//	outbound traffic — spans accumulate in the BatchSpanProcessor
//	queue and silently drop on shutdown timeout. The operator sees a
//	working `forge run` locally and an inexplicably empty trace
//	backend in cluster. The build pipeline must inject the collector
//	host into the allowlist automatically so "tracing on in
//	forge.yaml" implies "tracing reaches the backend" without a
//	second egress edit.
//
// The function returns nil when tracing is disabled, when no endpoint
// is configured, or when the endpoint is unparseable — every "skip"
// case yields an empty slice the caller appends as a no-op. A
// malformed endpoint is NOT a build-time error here; the cli's tracing
// resolver (Phase 2) is the single place that fails loudly on bad
// configuration, and Phase 6 is intentionally tolerant so the build
// stage can never block a deployment over telemetry config.
//
// Port stripping is handled by hostFromURL (see auth_domains.go for
// the cross-package contract — every egress matcher callsite strips
// the port from the OUTBOUND host before checking the allowlist, so a
// hostname-only entry suffices for any port).
//
// Source tagging: this helper returns bare hostnames matching the
// AuthDomains / MCPDomains convention. The egress_allowlist.json
// shape does not currently carry per-domain source provenance — a
// future allowlist schema upgrade could introduce a "source: otel"
// tag, mirroring MCPDomainSources, without changing this helper.
func OTelDomain(cfg types.TracingYAML) []string {
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil
	}
	host := hostFromURL(cfg.Endpoint)
	if host == "" {
		return nil
	}
	return []string{host}
}
