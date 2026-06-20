package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// otelEnvPassthroughPrefixes lists the OTel SDK env var prefixes / names
// the skill subprocess inherits unchanged from the parent process. This is
// the curated subset that affects exporter destination + sampling +
// service identity — knobs the child needs to keep its spans landing in
// the same backend the agent's spans land in.
//
// Deliberately excluded:
//
//   - OTEL_EXPORTER_OTLP_HEADERS / OTEL_EXPORTER_OTLP_TRACES_HEADERS —
//     can carry collector auth tokens; treating them like API keys means
//     they go through the same skill-declared env.optional path every
//     other secret does. Operators who need a shared collector header on
//     the child declare it explicitly.
//   - Anything OTEL_LOG_* / OTEL_BSP_* / OTEL_BLRP_* — span-batch tuning
//     that's process-local; no value crossing the fork.
//
// Issue #182.
var otelEnvPassthroughPrefixes = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	"OTEL_EXPORTER_OTLP_INSECURE",
	"OTEL_EXPORTER_OTLP_TRACES_INSECURE",
	"OTEL_SERVICE_NAME",
	"OTEL_RESOURCE_ATTRIBUTES",
	"OTEL_TRACES_SAMPLER",
	"OTEL_TRACES_SAMPLER_ARG",
	"OTEL_PROPAGATORS",
	"OTEL_SDK_DISABLED",
}

// OSCommandExecutor implements tools.CommandExecutor using os/exec.
type OSCommandExecutor struct{}

func (e *OSCommandExecutor) Run(ctx context.Context, command string, args []string, stdin []byte) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("command error: %s", stderr.String())
		}
		return "", fmt.Errorf("command execution failed: %w", err)
	}

	return stdout.String(), nil
}

// SkillCommandExecutor implements tools.CommandExecutor with a configurable
// timeout and environment variable passthrough for skill scripts.
type SkillCommandExecutor struct {
	Timeout  time.Duration
	WorkDir  string   // agent working directory — script paths are relative to this
	EnvVars  []string // extra env var names to pass through (e.g., "TAVILY_API_KEY")
	ProxyURL string   // egress proxy URL (e.g., "http://127.0.0.1:54321")
	Model    string   // configured LLM model name — passed as REVIEW_MODEL to skill scripts
}

func (e *SkillCommandExecutor) Run(ctx context.Context, command string, args []string, stdin []byte) (string, error) {
	timeout := e.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	if e.WorkDir != "" {
		cmd.Dir = e.WorkDir
	}

	// Build minimal environment with only explicitly allowed variables.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	for _, name := range e.EnvVars {
		if val := os.Getenv(name); val != "" {
			// Resolve OAuth sentinel to actual access token so skill
			// scripts can call the LLM API directly via curl.
			if val == "__oauth__" && name == "OPENAI_API_KEY" {
				if resolved := resolveOAuthToken(); resolved != nil {
					val = resolved.AccessToken
					if resolved.BaseURL != "" {
						env = append(env, "OPENAI_BASE_URL="+resolved.BaseURL)
					}
				}
			}
			env = append(env, name+"="+val)
		}
	}
	if orgID := os.Getenv("OPENAI_ORG_ID"); orgID != "" {
		env = append(env, "OPENAI_ORG_ID="+orgID)
	}
	// Issue #137 — always-pass standard provider base URL env vars when
	// present in the parent env. These are the canonical SDK-recognized
	// variables operators use to redirect provider-shape API calls to a
	// compatible host (Together.ai, OpenRouter, Groq, Fireworks,
	// Anyscale via OPENAI_BASE_URL; Bedrock proxy via
	// ANTHROPIC_BASE_URL; remotely-served Ollama via OLLAMA_BASE_URL).
	// Treating them like OPENAI_ORG_ID above means every skill that
	// calls an LLM API directly just works for compatible-provider
	// deployments without each SKILL.md author having to remember to
	// declare them in env.optional. Pre-fix every such skill silently
	// hit the wrong (default-OpenAI) endpoint.
	for _, name := range []string{
		"OPENAI_BASE_URL",
		"ANTHROPIC_BASE_URL",
		"OLLAMA_BASE_URL",
		"GEMINI_BASE_URL",
	} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	// Pass configured model to skill scripts (e.g., code-review uses REVIEW_MODEL).
	// This ensures OAuth/Codex users get a compatible model instead of the
	// script's default (gpt-4o) which may not be supported.
	if e.Model != "" && os.Getenv("REVIEW_MODEL") == "" {
		env = append(env, "REVIEW_MODEL="+e.Model)
	}
	if e.ProxyURL != "" {
		env = append(env,
			"HTTP_PROXY="+e.ProxyURL,
			"HTTPS_PROXY="+e.ProxyURL,
			"http_proxy="+e.ProxyURL,
			"https_proxy="+e.ProxyURL,
		)
	}
	// Issue #182 — propagate W3C trace context + curated OTel SDK env
	// vars so the subprocess's spans nest under the parent agent's
	// `tool.<name>` span. Without this the child starts a fresh root
	// trace and disappears from the agent's call tree.
	//
	// The global propagator was installed at runtime startup
	// (forge-core/runtime/tracing.go) with TraceContext + Baggage. When
	// tracing is off the propagator is a no-op composite and Inject
	// writes nothing — the child sees no TRACEPARENT env and behaves
	// identically to pre-#182.
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	for _, k := range []struct{ header, envKey string }{
		{"traceparent", "TRACEPARENT"},
		{"tracestate", "TRACESTATE"},
		{"baggage", "BAGGAGE"},
	} {
		if v := carrier.Get(k.header); v != "" {
			env = append(env, k.envKey+"="+v)
		}
	}
	// OTel SDK config — endpoint, protocol, samplers, service name —
	// pass through unchanged so the child exports to the same backend
	// with consistent sampling. See otelEnvPassthroughPrefixes for the
	// curated allowlist and the deliberate exclusions.
	for _, name := range otelEnvPassthroughPrefixes {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("skill command error: %s", stderr.String())
		}
		return "", fmt.Errorf("skill command execution failed: %w", err)
	}

	return stdout.String(), nil
}

// resolveOAuthToken loads and refreshes the OpenAI OAuth token.
// Returns nil if no valid token is available.
func resolveOAuthToken() *oauth.Token {
	token, err := oauth.LoadCredentials("openai")
	if err != nil || token == nil {
		return nil
	}
	if token.IsExpired() && token.RefreshToken != "" {
		oauthCfg := oauth.OpenAIConfig()
		newToken, rErr := oauth.RefreshToken(oauthCfg.TokenURL, oauthCfg.ClientID, token.RefreshToken)
		if rErr != nil {
			return nil
		}
		if newToken.RefreshToken == "" {
			newToken.RefreshToken = token.RefreshToken
		}
		if newToken.BaseURL == "" {
			newToken.BaseURL = token.BaseURL
		}
		_ = oauth.SaveCredentials("openai", newToken)
		return newToken
	}
	return token
}
