package security

import (
	"sort"

	"github.com/initializ/forge/forge-core/types"
)

// APIDomains extracts the outbound hosts that API servers in forge.yaml
// apis.servers[] must be reachable on. Mirrors MCPDomains: without this merge
// into the egress allowlist, an api-tool's outbound REST call would be silently
// blocked by the egress enforcer. Only the base_url host is needed (bearer/
// static auth — no separate OAuth AS host). Deduped + sorted for stable output.
func APIDomains(cfg types.APIConfig) []string {
	if len(cfg.Servers) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range cfg.Servers {
		if h := hostFromURL(s.BaseURL); h != "" {
			seen[h] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// APIDomainSources tags each API host with "api:<server-name>" for egress
// allowlist provenance (mirrors MCPDomainSources). First server name wins.
func APIDomainSources(cfg types.APIConfig) map[string]string {
	if len(cfg.Servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Servers))
	baseByName := map[string]string{}
	for _, s := range cfg.Servers {
		names = append(names, s.Name)
		baseByName[s.Name] = s.BaseURL
	}
	sort.Strings(names)
	out := map[string]string{}
	for _, name := range names {
		h := hostFromURL(baseByName[name])
		if h == "" {
			continue
		}
		if _, exists := out[h]; !exists {
			out[h] = "api:" + name
		}
	}
	return out
}
