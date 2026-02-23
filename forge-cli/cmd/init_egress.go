package cmd

import (
	"sort"

	skillreg "github.com/initializ/forge/forge-core/registry"
	"github.com/initializ/forge/forge-core/security"
)

// providerDomains maps model provider names to their API domains.
var providerDomains = map[string]string{
	"openai":    "api.openai.com",
	"anthropic": "api.anthropic.com",
	"gemini":    "generativelanguage.googleapis.com",
	// ollama is local, no egress needed
}

// deriveEgressDomains computes the full set of egress domains needed based on
// the provider, channels, builtin tools, and selected registry skills.
func deriveEgressDomains(opts *initOptions, skills []skillreg.SkillInfo) []string {
	seen := make(map[string]bool)
	var domains []string

	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}

	// 1. Provider domain
	if d, ok := providerDomains[opts.ModelProvider]; ok {
		add(d)
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
