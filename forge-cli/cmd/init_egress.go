package cmd

import (
	"sort"

	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-skills/contract"
)

// providerDomains maps model provider names to their API domains.
var providerDomains = map[string]string{
	"openai":    "api.openai.com",
	"anthropic": "api.anthropic.com",
	"gemini":    "generativelanguage.googleapis.com",
	// ollama is local, no egress needed
}

// mergeEgressDomains returns the union of `base` and `extra`, preserving
// the order of `base` and appending only previously-unseen items from
// `extra`. The result is deduplicated and stable.
//
// Used by the init scaffold to fold auth-provider hosts (from the wizard
// or --auth flags) into the egress allowlist without disturbing the
// existing ordering.
func mergeEgressDomains(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, d := range base {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, d := range extra {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// deriveEgressDomains computes the full set of egress domains needed based on
// the provider, channels, builtin tools, and selected registry skills.
func deriveEgressDomains(opts *initOptions, skills []contract.SkillDescriptor) []string {
	seen := make(map[string]bool)
	var domains []string

	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}

	// 1. Provider domains (primary + fallbacks)
	if d, ok := providerDomains[opts.ModelProvider]; ok {
		add(d)
	}
	for _, fb := range opts.Fallbacks {
		if d, ok := providerDomains[fb.Provider]; ok {
			add(d)
		}
	}

	// 2. Channel domains
	for _, d := range security.ResolveCapabilities(opts.Channels) {
		add(d)
	}

	// 3. Tool domains (web_search filtered by provider)
	for _, toolName := range opts.BuiltinTools {
		if toolName == "web_search" || toolName == "web-search" {
			provider := opts.EnvVars["WEB_SEARCH_PROVIDER"]
			switch provider {
			case "perplexity":
				add("api.perplexity.ai")
			default:
				add("api.tavily.com")
			}
			continue
		}
		for _, d := range security.DefaultToolDomains[toolName] {
			add(d)
		}
	}

	// 4. Skill domains
	for _, s := range skills {
		for _, d := range s.EgressDomains {
			add(d)
		}
	}

	sort.Strings(domains)
	return domains
}
