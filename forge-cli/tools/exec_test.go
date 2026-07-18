package tools

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func TestSkillCommandExecutor_OrgIDInjection(t *testing.T) {
	// Set the env var
	t.Setenv("OPENAI_ORG_ID", "org-test-skill-123")

	e := &SkillCommandExecutor{}

	// Run a command that prints environment variables
	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "OPENAI_ORG_ID=org-test-skill-123") {
		t.Errorf("expected OPENAI_ORG_ID in env output, got: %s", out)
	}
}

func TestSkillCommandExecutor_NoOrgIDWhenUnset(t *testing.T) {
	// Ensure the env var is NOT set
	os.Unsetenv("OPENAI_ORG_ID") //nolint:errcheck

	e := &SkillCommandExecutor{}

	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(out, "OPENAI_ORG_ID") {
		t.Errorf("expected no OPENAI_ORG_ID in env output, got: %s", out)
	}
}

// TestSkillCommandExecutor_ProviderBaseURLs_AlwaysPassed pins the
// Issue #137 invariant: the standard provider base URL env vars
// (OPENAI_BASE_URL, ANTHROPIC_BASE_URL, OLLAMA_BASE_URL,
// GEMINI_BASE_URL) MUST flow to every skill subprocess when they
// are set in the parent env — without each skill having to declare
// them in its SKILL.md env.optional. These are SDK-recognized
// standard variables for redirecting provider-shape API calls to
// compatible hosts (Together.ai, OpenRouter, Groq, Fireworks,
// Anyscale, remote Ollama, etc.). Pre-fix every such skill silently
// hit the wrong (default-OpenAI) endpoint.
//
// Why one test, not four: the always-passed surface is a single
// allowlist; running env-print once with all four set covers the
// invariant without needing a process spawn per variable.
func TestSkillCommandExecutor_ProviderBaseURLs_AlwaysPassed(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://api.together.ai/v1")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic-proxy.internal/v1")
	t.Setenv("OLLAMA_BASE_URL", "http://ollama.svc.cluster.local:11434")
	t.Setenv("GEMINI_BASE_URL", "https://gemini-proxy.internal/v1")

	// Empty EnvVars whitelist — the skill did NOT declare these in its
	// SKILL.md env.optional. The fix must pass them through anyway.
	e := &SkillCommandExecutor{}

	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"OPENAI_BASE_URL=https://api.together.ai/v1",
		"ANTHROPIC_BASE_URL=https://anthropic-proxy.internal/v1",
		"OLLAMA_BASE_URL=http://ollama.svc.cluster.local:11434",
		"GEMINI_BASE_URL=https://gemini-proxy.internal/v1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in skill subprocess env (issue #137); got:\n%s", want, out)
		}
	}
}

// TestSkillCommandExecutor_ProviderBaseURLs_NotEmittedWhenUnset
// confirms the omit-when-empty semantic: if the parent env doesn't
// have one of these vars set, the subprocess env doesn't gain an
// empty-value line for it. Matches the OPENAI_ORG_ID precedent
// above. The fix must be an allowlist, not a hardcoded forward.
func TestSkillCommandExecutor_ProviderBaseURLs_NotEmittedWhenUnset(t *testing.T) {
	os.Unsetenv("OPENAI_BASE_URL")    //nolint:errcheck
	os.Unsetenv("ANTHROPIC_BASE_URL") //nolint:errcheck
	os.Unsetenv("OLLAMA_BASE_URL")    //nolint:errcheck
	os.Unsetenv("GEMINI_BASE_URL")    //nolint:errcheck

	e := &SkillCommandExecutor{}

	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, forbidden := range []string{
		"OPENAI_BASE_URL=",
		"ANTHROPIC_BASE_URL=",
		"OLLAMA_BASE_URL=",
		"GEMINI_BASE_URL=",
	} {
		// Use a line-anchored check via newline boundaries so we don't
		// false-positive on a substring that appears inside a value.
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, forbidden) {
				t.Errorf("unset provider base URL %s leaked into subprocess env as %q",
					forbidden, line)
			}
		}
	}
}

// traceparentRE matches the W3C traceparent header format:
// "<version>-<32hex traceid>-<16hex spanid>-<2hex flags>".
var traceparentRE = regexp.MustCompile(`^TRACEPARENT=00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// installTestPropagator wires the global propagator + a tracer provider
// the test owns. Returns the tracer so callers can open a span on ctx.
// Issue #182 — the production install lives in
// forge-core/runtime/tracing.go but this package's tests can't take a
// dep on that file (cycle); duplicating the propagator install here is
// the minimum needed to exercise propagation.MapCarrier.Inject.
func installTestPropagator(t *testing.T) {
	t.Helper()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// TestSkillCommandExecutor_TraceparentInjectedWhenCtxHasSpan is the
// issue #182 pin: when the parent agent has opened a `tool.<name>`
// span and passes ctx into SkillCommandExecutor.Run, the subprocess
// MUST receive a TRACEPARENT env var whose trace-id matches the
// parent span's trace-id. Without this the child's spans start a
// fresh root and disappear from the agent's call tree.
func TestSkillCommandExecutor_TraceparentInjectedWhenCtxHasSpan(t *testing.T) {
	installTestPropagator(t)
	rec := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(rec))
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "tool.test_skill")
	defer span.End()

	e := &SkillCommandExecutor{}
	out, err := e.Run(ctx, "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var found string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "TRACEPARENT=") {
			found = line
			break
		}
	}
	if found == "" {
		t.Fatalf("subprocess env missing TRACEPARENT line; got:\n%s", out)
	}
	if !traceparentRE.MatchString(found) {
		t.Errorf("TRACEPARENT not W3C-shaped: %q", found)
	}
	// The injected trace-id MUST be the parent span's trace-id.
	wantTrace := span.SpanContext().TraceID().String()
	if !strings.Contains(found, wantTrace) {
		t.Errorf("TRACEPARENT carries wrong trace-id: got %q, want trace-id %s",
			found, wantTrace)
	}
}

// TestSkillCommandExecutor_TraceparentAbsentWhenNoSpan confirms the
// no-tracing case: when ctx carries no active span, the subprocess
// env gains no TRACEPARENT line. Pre-#182 deployments that don't
// enable tracing must see byte-identical pre-fix env.
func TestSkillCommandExecutor_TraceparentAbsentWhenNoSpan(t *testing.T) {
	installTestPropagator(t)
	e := &SkillCommandExecutor{}
	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "TRACEPARENT=") {
			t.Errorf("TRACEPARENT leaked into subprocess env when ctx had no span: %q", line)
		}
	}
}

// TestSkillCommandExecutor_OTelSubsetPassedThrough pins the curated
// allowlist: standard OTel SDK config (endpoint, protocol, service
// name, samplers, resource attrs, propagators) flows to the
// subprocess so the child exports to the same collector with
// matching sampling. Pinned alongside the explicit exclusion of
// OTEL_EXPORTER_OTLP_HEADERS — collector auth headers must NOT leak
// through the blanket allowlist; operators who need them on the
// child declare them via env.optional like every other secret.
func TestSkillCommandExecutor_OTelSubsetPassedThrough(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.observability:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_SERVICE_NAME", "forge-agent")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.1")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=prod")
	t.Setenv("OTEL_PROPAGATORS", "tracecontext,baggage")
	// Auth-bearing var that MUST NOT pass through.
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer secret-collector-token")

	e := &SkillCommandExecutor{}
	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustHave := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_SERVICE_NAME=forge-agent",
		"OTEL_TRACES_SAMPLER=parentbased_traceidratio",
		"OTEL_TRACES_SAMPLER_ARG=0.1",
		"OTEL_RESOURCE_ATTRIBUTES=deployment.environment=prod",
		"OTEL_PROPAGATORS=tracecontext,baggage",
	}
	for _, want := range mustHave {
		if !strings.Contains(out, want) {
			t.Errorf("expected OTel passthrough %q in subprocess env; got:\n%s", want, out)
		}
	}
	// Security pin: collector auth headers must NOT leak.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "OTEL_EXPORTER_OTLP_HEADERS=") {
			t.Errorf("OTEL_EXPORTER_OTLP_HEADERS leaked into subprocess env (carries auth tokens): %q", line)
		}
	}
}

// TestProxyURLWithIdentity covers the userinfo stamping that lets the egress
// proxy attribute a subprocess request to its task/invocation (#338).
func TestProxyURLWithIdentity(t *testing.T) {
	const base = "http://127.0.0.1:54321"
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "both ids stamped as userinfo",
			ctx:  coreruntime.WithCorrelationID(coreruntime.WithTaskID(context.Background(), "task-1"), "corr-1"),
			want: "http://task-1:corr-1@127.0.0.1:54321",
		},
		{
			name: "task only",
			ctx:  coreruntime.WithTaskID(context.Background(), "task-1"),
			want: "http://task-1:@127.0.0.1:54321",
		},
		{
			name: "no identity leaves base unchanged",
			ctx:  context.Background(),
			want: base,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxyURLWithIdentity(tc.ctx, base); got != tc.want {
				t.Errorf("proxyURLWithIdentity() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSkillCommandExecutor_ProxyIdentityInjected proves the executor injects
// the identity-bearing proxy URL into HTTP_PROXY when ctx carries task context.
func TestSkillCommandExecutor_ProxyIdentityInjected(t *testing.T) {
	e := &SkillCommandExecutor{ProxyURL: "http://127.0.0.1:9"}
	ctx := coreruntime.WithCorrelationID(coreruntime.WithTaskID(context.Background(), "task-42"), "corr-42")
	out, err := e.Run(ctx, "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "HTTP_PROXY=http://task-42:corr-42@127.0.0.1:9") {
		t.Errorf("expected identity-stamped HTTP_PROXY in subprocess env; got:\n%s", out)
	}
}
