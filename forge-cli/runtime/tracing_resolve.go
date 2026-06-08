package runtime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/observability"
	"github.com/initializ/forge/forge-core/types"
)

// TracingFlags carries the CLI-flag-derived tracing overrides. Pointers
// so the resolver can distinguish "flag not passed" from "explicitly
// zero" — `--otel-sampler-ratio 0` is a legitimate ask (drop everything),
// distinct from "no --otel-sampler-ratio flag".
//
// Populated by forge-cli/cmd/run.go from the `--otel-*` flag variables;
// passed through RunnerConfig.TracingFlags into ResolveTracingConfig.
// Nil = "no CLI overrides" — equivalent to a zero-value struct.
type TracingFlags struct {
	Enabled        *bool
	Endpoint       *string
	Protocol       *string
	Sampler        *string
	SamplerRatio   *float64
	Timeout        *time.Duration
	ServiceName    *string
	CaptureContent *bool
	Redact         *bool
}

// ResolveTracingConfig folds three sources into one
// observability.TracingConfig that observability.NewTracerProvider can
// consume. Precedence, lowest → highest:
//
//  1. Built-in defaults (DefaultProtocol / DefaultSampler / ...)
//  2. `observability.tracing:` in forge.yaml (types.TracingYAML)
//  3. OTEL_* environment variables (the standard ones every OTel SDK
//     reads — operators arrive with these already in muscle memory)
//  4. CLI flags (--otel-*; a deploy-time override that wins over yaml
//     and env)
//
// Two derived fields the operator does not set themselves:
//
//   - ServiceName falls back to agentID when nothing else supplies one.
//   - ServiceVersion is copied from agentVersion (the agent's
//     forge.yaml `version:`).
//   - RuntimeVersion is the Forge cli's own build version, surfaced as
//     the `forge.runtime.version` resource attribute.
//
// Pure function — no side effects, no logging. Caller decides what to
// do with the result (typically: feed it to observability.NewTracerProvider,
// which returns ErrDisabled when Enabled is false or Endpoint is empty).
func ResolveTracingConfig(
	yamlCfg types.TracingYAML,
	flags TracingFlags,
	agentID, agentVersion, runtimeVersion string,
) observability.TracingConfig {
	// Start with yaml — it is the most stable source.
	out := observability.TracingConfig{
		Enabled:        yamlCfg.Enabled,
		Endpoint:       yamlCfg.Endpoint,
		Protocol:       yamlCfg.Protocol,
		Sampler:        yamlCfg.Sampler,
		SamplerRatio:   yamlCfg.SamplerRatio,
		Headers:        copyStringMap(yamlCfg.Headers),
		Timeout:        yamlCfg.Timeout,
		ServiceName:    yamlCfg.ServiceName,
		ResourceAttrs:  copyStringMap(yamlCfg.ResourceAttrs),
		CaptureContent: yamlCfg.CaptureContent,
		// Redact defaults true; honor yaml override when present.
		Redact: yamlCfg.Redact == nil || *yamlCfg.Redact,
	}

	// 2. Environment variables — the standard OTel SDK names.
	applyTracingEnv(&out)

	// 3. CLI flags win over yaml + env.
	if flags.Enabled != nil {
		out.Enabled = *flags.Enabled
	}
	if flags.Endpoint != nil {
		out.Endpoint = *flags.Endpoint
	}
	if flags.Protocol != nil {
		out.Protocol = *flags.Protocol
	}
	if flags.Sampler != nil {
		out.Sampler = *flags.Sampler
	}
	if flags.SamplerRatio != nil {
		out.SamplerRatio = *flags.SamplerRatio
	}
	if flags.Timeout != nil {
		out.Timeout = *flags.Timeout
	}
	if flags.ServiceName != nil {
		out.ServiceName = *flags.ServiceName
	}
	if flags.CaptureContent != nil {
		out.CaptureContent = *flags.CaptureContent
	}
	if flags.Redact != nil {
		out.Redact = *flags.Redact
	}

	// 4. Final derived fallbacks — ServiceName + ServiceVersion +
	// RuntimeVersion. These can only be filled here because the
	// resolver is the first layer that sees both the agent's config
	// (for AgentID / Version) and the cli build version.
	if out.ServiceName == "" {
		out.ServiceName = agentID
	}
	out.ServiceVersion = agentVersion
	out.RuntimeVersion = runtimeVersion
	return out
}

// applyTracingEnv overlays the standard OTel SDK env vars onto the
// (already yaml-populated) TracingConfig. Empty values are ignored so
// `OTEL_EXPORTER_OTLP_ENDPOINT=""` does NOT wipe a yaml-configured
// endpoint — the absence of a value is "no override," not "unset."
//
// Env vars honored:
//
//	OTEL_SDK_DISABLED              bool   → Enabled (inverted)
//	OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
//	                               string → Endpoint  (preferred — traces-specific)
//	OTEL_EXPORTER_OTLP_ENDPOINT    string → Endpoint  (fallback — applies to every signal)
//	OTEL_EXPORTER_OTLP_PROTOCOL    string → Protocol
//	OTEL_EXPORTER_OTLP_HEADERS     csv    → Headers (merged with yaml)
//	OTEL_EXPORTER_OTLP_TIMEOUT     ms     → Timeout
//	OTEL_SERVICE_NAME              string → ServiceName
//	OTEL_RESOURCE_ATTRIBUTES       csv    → ResourceAttrs (merged with yaml)
//	OTEL_TRACES_SAMPLER            string → Sampler
//	OTEL_TRACES_SAMPLER_ARG        float  → SamplerRatio
func applyTracingEnv(cfg *observability.TracingConfig) {
	if v := os.Getenv("OTEL_SDK_DISABLED"); v != "" {
		if disabled, err := strconv.ParseBool(v); err == nil {
			cfg.Enabled = !disabled
		}
	}
	// Traces-specific endpoint takes precedence over the generic one.
	if v := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	} else if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"); v != "" {
		cfg.Protocol = v
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"); v != "" {
		cfg.Headers = mergeStringMap(cfg.Headers, parseHeaderCSV(v))
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_TIMEOUT"); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			cfg.Timeout = time.Duration(ms) * time.Millisecond
		}
	}
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	if v := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"); v != "" {
		cfg.ResourceAttrs = mergeStringMap(cfg.ResourceAttrs, parseHeaderCSV(v))
	}
	if v := os.Getenv("OTEL_TRACES_SAMPLER"); v != "" {
		cfg.Sampler = v
	}
	if v := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.SamplerRatio = f
		}
	}
}

// parseHeaderCSV splits "k1=v1,k2=v2" into a map. The same syntax is
// used by OTEL_EXPORTER_OTLP_HEADERS and OTEL_RESOURCE_ATTRIBUTES; the
// OTel spec is permissive on whitespace, so we trim. Malformed entries
// (no `=`) are dropped — Phase 2 of an initial OTel adoption is not the
// right place to fail startup on a typo in a non-load-bearing knob.
func parseHeaderCSV(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 || eq == len(pair)-1 {
			continue
		}
		k := strings.TrimSpace(pair[:eq])
		v := strings.TrimSpace(pair[eq+1:])
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// mergeStringMap returns a NEW map containing all entries from base with
// extra's entries overlaid (extra wins). Tolerates nil inputs.
func mergeStringMap(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func copyStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// FormatTracingStartupLine produces a one-line human-readable summary
// of the resolved config for the runner's ops log at startup. Excludes
// Headers (may contain secrets) and ResourceAttrs (often noisy); the
// audit trail and the OTel collector itself are the load-bearing
// surfaces for those.
func FormatTracingStartupLine(cfg observability.TracingConfig) string {
	if !cfg.Enabled || cfg.Endpoint == "" {
		return "tracing disabled"
	}
	return fmt.Sprintf(
		"tracing endpoint=%s protocol=%s sampler=%s ratio=%.2f service=%s",
		cfg.Endpoint, cfg.Protocol, cfg.Sampler, cfg.SamplerRatio, cfg.ServiceName,
	)
}
