package security

import (
	"sort"

	"github.com/initializ/forge/forge-core/types"
)

// MCPDomains extracts the outbound hosts that MCP servers in
// forge.yaml mcp.servers[] must be reachable on. Mirrors AuthDomains
// for the same reason: without this merge into the egress allowlist,
// an HTTP MCP call would be silently blocked by the egress enforcer.
//
// Hosts are deduplicated and sorted for stable test output.
// Empty/malformed URLs are skipped — validation happens in
// validate.ValidateMCPConfig.
//
// Phase 1: HTTP transport only, so the only outbound is the server's
// URL host. Future OAuth-discovery work may add an authorization-
// server host once we land RFC 9728 discovery.
func MCPDomains(cfg types.MCPConfig) []string {
	if len(cfg.Servers) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range cfg.Servers {
		// Server's MCP endpoint.
		if h := hostFromURL(s.URL); h != "" {
			seen[h] = struct{}{}
		}
		// OAuth endpoints (when configured) — operator-declared in
		// Phase 1. Allowlist needs both authorize and token URLs since
		// laptop-time login uses authorize_url and runtime refresh
		// uses token_url.
		if s.Auth != nil && s.Auth.Type == "oauth" {
			if h := hostFromURL(s.Auth.AuthorizeURL); h != "" {
				seen[h] = struct{}{}
			}
			if h := hostFromURL(s.Auth.TokenURL); h != "" {
				seen[h] = struct{}{}
			}
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

// MCPDomainSources returns a per-domain source tag suitable for
// embedding in egress_allowlist.json provenance. Tags look like
// "mcp:<server-name>" and let operators trace why a given domain is
// in the allowlist (vs. tool-derived or operator-supplied).
//
// When the same host appears via multiple servers (e.g., a shared
// OAuth authorization server across two MCP services), the tag is
// the lexicographically first server name — deterministic.
func MCPDomainSources(cfg types.MCPConfig) map[string]string {
	if len(cfg.Servers) == 0 {
		return nil
	}
	// Sort server names so the deterministic-first rule is obvious.
	names := make([]string, 0, len(cfg.Servers))
	specByName := map[string]types.MCPServer{}
	for _, s := range cfg.Servers {
		names = append(names, s.Name)
		specByName[s.Name] = s
	}
	sort.Strings(names)

	out := map[string]string{}
	for _, name := range names {
		s := specByName[name]
		urls := []string{s.URL}
		if s.Auth != nil && s.Auth.Type == "oauth" {
			urls = append(urls, s.Auth.AuthorizeURL, s.Auth.TokenURL)
		}
		for _, raw := range urls {
			h := hostFromURL(raw)
			if h == "" {
				continue
			}
			if _, exists := out[h]; !exists {
				out[h] = "mcp:" + name
			}
		}
	}
	return out
}
