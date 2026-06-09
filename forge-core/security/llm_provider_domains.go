package security

import "github.com/initializ/forge/forge-core/types"

// LLMProviderDomains returns the hostnames of every custom base URL
// declared on the agent's primary model and its fallbacks. Used by the
// build pipeline (forge-cli/build/egress_stage.go) and the runner
// (forge-cli/runtime/runner.go) to auto-merge LLM provider hosts into
// the egress allowlist alongside AuthDomains, MCPDomains, and
// OTelDomain.
//
// Why this exists (issue #139):
//
//	Without this, an agent configured against an OpenAI-compatible
//	provider (Together.ai, OpenRouter, Groq, Fireworks, Anyscale,
//	vLLM, llama.cpp's server) ships a NetworkPolicy that blocks the
//	provider's hostname — the build pipeline only sees what's in
//	forge.yaml, and the operator's env-driven OPENAI_BASE_URL doesn't
//	flow through. Same trap Phase 6 of OTel Tracing v1 (#107) fixed
//	for the OTLP collector; this is the symmetric fix for the LLM
//	provider.
//
// Returns nil when neither the primary nor any fallback declares a
// base URL — backward-compatible with deployments that rely on the
// vendor's default host (api.openai.com, api.anthropic.com, etc.)
// already covered by the operator's explicit egress.allowed_domains.
//
// Malformed URLs are silently skipped (same posture as AuthDomains /
// MCPDomains / OTelDomain): the build pipeline must never block a
// deployment over LLM config; the runtime config resolver is the
// single place that fails loudly on bad URLs.
//
// Port stripping follows the cross-package contract documented on
// hostFromURL — every egress matcher callsite strips the port before
// checking the allowlist, so a hostname-only entry suffices.
func LLMProviderDomains(cfg *types.ForgeConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		if raw == "" {
			return
		}
		host := hostFromURL(raw)
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		out = append(out, host)
	}
	add(cfg.Model.BaseURL)
	for _, fb := range cfg.Model.Fallbacks {
		add(fb.BaseURL)
	}
	return out
}

// LLMProviderEnvDomains returns the hostnames extracted from the four
// canonical SDK base-URL env vars when present in the supplied env
// map. Used by the runner to auto-merge env-driven LLM provider hosts
// into the egress allowlist for deployments that haven't yet migrated
// to the new ModelRef.BaseURL field.
//
// Why two helpers, not one:
//
//	LLMProviderDomains is the build-time signal (read from forge.yaml,
//	the only source the build pipeline can see). LLMProviderEnvDomains
//	is the runtime safety-net (read from the resolved env). Most
//	operators will populate forge.yaml going forward; the env-based
//	helper rescues existing deployments that point at a custom
//	provider via OPENAI_BASE_URL only.
//
// The env vars consulted are the standard SDK conventions every
// OpenAI/Anthropic/Ollama/Gemini-compatible provider documents.
// Forge does not invent any Forge-specific FORGE_*_BASE_URL variant.
//
// Returns nil when none are set. Malformed URLs are silently
// skipped (same posture as the cfg-side helper).
func LLMProviderEnvDomains(envVars map[string]string) []string {
	if len(envVars) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, key := range []string{
		"OPENAI_BASE_URL",
		"ANTHROPIC_BASE_URL",
		"OLLAMA_BASE_URL",
		"GEMINI_BASE_URL",
	} {
		host := hostFromURL(envVars[key])
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}
