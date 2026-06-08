package runtime

import (
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/observability"
	"github.com/initializ/forge/forge-core/types"
)

// ─── Precedence: defaults → yaml → env → flags ───────────────────────

func TestResolveTracingConfig_AllUnset_LeavesDisabled(t *testing.T) {
	cfg := ResolveTracingConfig(types.TracingYAML{}, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")

	if cfg.Enabled {
		t.Error("default Enabled must be false (off by default per #108 ruling)")
	}
	if cfg.ServiceName != "agent-x" {
		t.Errorf("ServiceName fallback = %q, want %q (agent_id)", cfg.ServiceName, "agent-x")
	}
	if cfg.ServiceVersion != "0.1.0" {
		t.Errorf("ServiceVersion = %q, want %q", cfg.ServiceVersion, "0.1.0")
	}
	if cfg.RuntimeVersion != "v0.13.0" {
		t.Errorf("RuntimeVersion = %q, want %q", cfg.RuntimeVersion, "v0.13.0")
	}
	if !cfg.Redact {
		t.Error("Redact must default true (PII-safe posture)")
	}
}

func TestResolveTracingConfig_YAMLOnly(t *testing.T) {
	redactFalse := false
	yamlCfg := types.TracingYAML{
		Enabled:        true,
		Endpoint:       "http://collector:4318/v1/traces",
		Protocol:       observability.ProtocolHTTPProtobuf,
		Sampler:        observability.SamplerTraceIDRatio,
		SamplerRatio:   0.1,
		Timeout:        5 * time.Second,
		ServiceName:    "explicit-name",
		Headers:        map[string]string{"x-tenant": "demo"},
		ResourceAttrs:  map[string]string{"deployment.environment": "staging"},
		Redact:         &redactFalse,
		CaptureContent: true,
	}
	cfg := ResolveTracingConfig(yamlCfg, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")

	if !cfg.Enabled || cfg.Endpoint != "http://collector:4318/v1/traces" {
		t.Errorf("yaml Enabled/Endpoint not carried; got %+v", cfg)
	}
	if cfg.ServiceName != "explicit-name" {
		t.Errorf("yaml ServiceName must NOT be overridden by agent_id fallback when set; got %q", cfg.ServiceName)
	}
	if cfg.SamplerRatio != 0.1 {
		t.Errorf("SamplerRatio = %v, want 0.1", cfg.SamplerRatio)
	}
	if cfg.Redact {
		t.Error("Redact pointer was explicit-false; must NOT be overridden to true")
	}
	if !cfg.CaptureContent {
		t.Error("CaptureContent carried as true")
	}
}

func TestResolveTracingConfig_EnvOverridesYAML(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://env-collector:4318")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	t.Setenv("OTEL_SERVICE_NAME", "env-service")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "15000")
	t.Setenv("OTEL_SDK_DISABLED", "false")

	yamlCfg := types.TracingYAML{
		Enabled:      true,
		Endpoint:     "http://yaml-collector:4318",
		Sampler:      observability.SamplerAlwaysOn,
		SamplerRatio: 1.0,
		ServiceName:  "yaml-service",
		Timeout:      2 * time.Second,
	}
	cfg := ResolveTracingConfig(yamlCfg, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")

	if cfg.Endpoint != "http://env-collector:4318" {
		t.Errorf("env Endpoint must override yaml; got %q", cfg.Endpoint)
	}
	if cfg.Sampler != observability.SamplerAlwaysOff {
		t.Errorf("env Sampler must override yaml; got %q", cfg.Sampler)
	}
	if cfg.SamplerRatio != 0.25 {
		t.Errorf("env SamplerRatio must override yaml; got %v", cfg.SamplerRatio)
	}
	if cfg.ServiceName != "env-service" {
		t.Errorf("env ServiceName must override yaml; got %q", cfg.ServiceName)
	}
	if cfg.Timeout != 15*time.Second {
		t.Errorf("env Timeout must override yaml; got %v", cfg.Timeout)
	}
	if !cfg.Enabled {
		t.Error("OTEL_SDK_DISABLED=false must NOT flip a yaml-enabled config off")
	}
}

func TestResolveTracingConfig_TracesEndpointBeatsGenericEndpoint(t *testing.T) {
	// OTel spec: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is signal-specific
	// and takes precedence over the generic OTEL_EXPORTER_OTLP_ENDPOINT.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://generic:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://traces:4318/v1/traces")

	cfg := ResolveTracingConfig(types.TracingYAML{Enabled: true}, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")
	if cfg.Endpoint != "http://traces:4318/v1/traces" {
		t.Errorf("traces-specific env must win; got %q", cfg.Endpoint)
	}
}

func TestResolveTracingConfig_FlagsOverrideEnvAndYAML(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://env-collector:4318")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	flagEnabled := true
	flagEndpoint := "http://flag-collector:4318"
	flagSampler := observability.SamplerParentBasedTraceIDRatio
	flagRatio := 0.5
	flagTimeout := 30 * time.Second
	flagService := "flag-service"
	flagCapture := true
	flagRedact := false

	yamlCfg := types.TracingYAML{
		Enabled:  false,
		Endpoint: "http://yaml-collector:4318",
		Sampler:  observability.SamplerAlwaysOn,
	}
	flags := TracingFlags{
		Enabled:        &flagEnabled,
		Endpoint:       &flagEndpoint,
		Sampler:        &flagSampler,
		SamplerRatio:   &flagRatio,
		Timeout:        &flagTimeout,
		ServiceName:    &flagService,
		CaptureContent: &flagCapture,
		Redact:         &flagRedact,
	}
	cfg := ResolveTracingConfig(yamlCfg, flags, "agent-x", "0.1.0", "v0.13.0")

	if !cfg.Enabled || cfg.Endpoint != "http://flag-collector:4318" {
		t.Errorf("flag Enabled/Endpoint must win over yaml+env; got %+v", cfg)
	}
	if cfg.Sampler != observability.SamplerParentBasedTraceIDRatio || cfg.SamplerRatio != 0.5 {
		t.Errorf("flag Sampler/Ratio must win; got %q/%v", cfg.Sampler, cfg.SamplerRatio)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("flag Timeout must win; got %v", cfg.Timeout)
	}
	if cfg.ServiceName != "flag-service" {
		t.Errorf("flag ServiceName must win; got %q", cfg.ServiceName)
	}
	if !cfg.CaptureContent {
		t.Error("flag CaptureContent must propagate")
	}
	if cfg.Redact {
		t.Error("flag Redact=false must propagate (over yaml's nil and env)")
	}
}

func TestResolveTracingConfig_DisabledFlagOverridesEnabledYAML(t *testing.T) {
	flagDisabled := false
	cfg := ResolveTracingConfig(
		types.TracingYAML{Enabled: true, Endpoint: "http://collector:4318"},
		TracingFlags{Enabled: &flagDisabled},
		"agent-x", "0.1.0", "v0.13.0",
	)
	if cfg.Enabled {
		t.Error("flag-disabled must override yaml-enabled")
	}
}

func TestResolveTracingConfig_EmptyEnvDoesNotWipeYAML(t *testing.T) {
	// Subtle: a set-but-empty env var must NOT override a non-empty
	// yaml field. The "absence of a value" semantic protects operators
	// from accidentally blanking telemetry by unsetting/clearing an env.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "")

	yamlCfg := types.TracingYAML{
		Enabled:     true,
		Endpoint:    "http://yaml-collector:4318",
		ServiceName: "yaml-service",
	}
	cfg := ResolveTracingConfig(yamlCfg, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")
	if cfg.Endpoint != "http://yaml-collector:4318" || cfg.ServiceName != "yaml-service" {
		t.Errorf("empty env must not wipe yaml; got %+v", cfg)
	}
}

// ─── Headers / resource attrs ────────────────────────────────────────

func TestResolveTracingConfig_EnvHeadersMergeWithYAML(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-env-tenant=demo, authorization=Bearer xyz")

	yamlCfg := types.TracingYAML{
		Enabled:  true,
		Endpoint: "http://collector:4318",
		Headers:  map[string]string{"x-yaml-header": "kept", "x-env-tenant": "WILL-BE-OVERRIDDEN"},
	}
	cfg := ResolveTracingConfig(yamlCfg, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")
	if cfg.Headers["x-yaml-header"] != "kept" {
		t.Errorf("yaml-only headers must survive env merge; got %v", cfg.Headers)
	}
	if cfg.Headers["x-env-tenant"] != "demo" {
		t.Errorf("env header must override yaml on key collision; got %v", cfg.Headers)
	}
	if cfg.Headers["authorization"] != "Bearer xyz" {
		t.Errorf("env header parsing dropped a value-with-space; got %v", cfg.Headers)
	}
}

func TestResolveTracingConfig_EnvResourceAttrsMergeWithYAML(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=prod, region=us-east-1")

	yamlCfg := types.TracingYAML{
		Enabled:       true,
		Endpoint:      "http://collector:4318",
		ResourceAttrs: map[string]string{"team": "platform", "deployment.environment": "staging"},
	}
	cfg := ResolveTracingConfig(yamlCfg, TracingFlags{}, "agent-x", "0.1.0", "v0.13.0")
	if cfg.ResourceAttrs["team"] != "platform" {
		t.Error("yaml-only attr must survive env merge")
	}
	if cfg.ResourceAttrs["deployment.environment"] != "prod" {
		t.Errorf("env attr must override yaml on collision; got %v", cfg.ResourceAttrs)
	}
	if cfg.ResourceAttrs["region"] != "us-east-1" {
		t.Error("env-only attr must be added")
	}
}

// ─── parseHeaderCSV edge cases ────────────────────────────────────────

func TestParseHeaderCSV_TolerantOfWhitespaceAndMalformed(t *testing.T) {
	got := parseHeaderCSV("  k1 = v1 , k2=v2,malformed,=, k3 = v with spaces ")
	if got["k1"] != "v1" {
		t.Errorf("trimmed key/val k1=v1 expected; got %v", got)
	}
	if got["k2"] != "v2" {
		t.Errorf("k2=v2 expected; got %v", got)
	}
	if _, ok := got["malformed"]; ok {
		t.Error("entries without `=` must be dropped")
	}
	if got["k3"] != "v with spaces" {
		t.Errorf("value internal spaces preserved after trim; got %v", got)
	}
	if len(got) != 3 {
		t.Errorf("expected exactly 3 valid entries; got %d (%v)", len(got), got)
	}
}

// ─── OTEL_SDK_DISABLED semantics ─────────────────────────────────────

func TestResolveTracingConfig_SDKDisabledTrueOverridesYAMLEnabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	cfg := ResolveTracingConfig(
		types.TracingYAML{Enabled: true, Endpoint: "http://collector:4318"},
		TracingFlags{},
		"agent-x", "0.1.0", "v0.13.0",
	)
	if cfg.Enabled {
		t.Error("OTEL_SDK_DISABLED=true must turn an otherwise-enabled config off")
	}
}

// ─── FormatTracingStartupLine ────────────────────────────────────────

func TestFormatTracingStartupLine_DisabledMessage(t *testing.T) {
	got := FormatTracingStartupLine(observability.TracingConfig{Enabled: false})
	if got != "tracing disabled" {
		t.Errorf("got %q", got)
	}
}

func TestFormatTracingStartupLine_EnabledShape(t *testing.T) {
	got := FormatTracingStartupLine(observability.TracingConfig{
		Enabled:      true,
		Endpoint:     "http://collector:4318",
		Protocol:     observability.ProtocolHTTPProtobuf,
		Sampler:      observability.SamplerParentBasedAlwaysOn,
		SamplerRatio: 1.0,
		ServiceName:  "agent-x",
	})
	// Don't pin the exact string — just confirm the load-bearing
	// fields are present, no secrets leak.
	for _, want := range []string{"http://collector:4318", "agent-x", "parentbased_always_on"} {
		if !contains(got, want) {
			t.Errorf("line must contain %q; got %q", want, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
