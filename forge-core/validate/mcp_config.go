package validate

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/types"
)

// MCP server name format. Same shape used by AWS account validators
// elsewhere in the codebase: lowercase slug, starts with a letter,
// ≤31 chars.
var mcpServerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// MCP tool name format. Allowed in Tools.Allow / Tools.Deny. Names
// containing "__" are reserved for the namespaced runtime form
// "<server>__<tool>"; here we only validate the bare tool name.
var mcpToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

// knownMCPTransports is the closed set of accepted transport values.
// Phase 1: HTTP only. Stdio is on the roadmap.
var knownMCPTransports = map[string]bool{"http": true}

// knownMCPAuthTypes is the closed set of accepted auth types.
var knownMCPAuthTypes = map[string]bool{"oauth": true, "bearer": true, "static": true}

// minMCPTimeout bounds the timeout knob. Anything tighter is almost
// certainly a misconfiguration; the default (set in the runtime when
// Timeout is zero) is 60s.
const minMCPTimeout = 1 * time.Second

// ValidateMCPConfig adds errors and warnings for a forge.yaml mcp:
// block. Empty MCPConfig is valid — agents without MCP servers
// continue to work unchanged.
func ValidateMCPConfig(cfg types.MCPConfig, r *ValidationResult) {
	if len(cfg.Servers) == 0 {
		return
	}

	seen := map[string]int{}
	for i, s := range cfg.Servers {
		prefix := fmt.Sprintf("mcp.servers[%d]", i)
		validateMCPServer(prefix, s, r)

		if s.Name != "" {
			seen[s.Name]++
			if seen[s.Name] > 1 {
				r.Errors = append(r.Errors,
					fmt.Sprintf("%s: duplicate name %q", prefix, s.Name))
			}
		}
	}
}

// validateMCPServer enforces the per-entry rules. Splitting it out
// keeps ValidateMCPConfig readable and lets us unit-test the rule set
// in isolation.
func validateMCPServer(prefix string, s types.MCPServer, r *ValidationResult) {
	// Name: required, slug format.
	if s.Name == "" {
		r.Errors = append(r.Errors, prefix+": name is required")
	} else if !mcpServerNamePattern.MatchString(s.Name) {
		r.Errors = append(r.Errors,
			fmt.Sprintf("%s: name %q must match ^[a-z][a-z0-9-]{0,30}$", prefix, s.Name))
	}

	// Transport: required, HTTP-only in Phase 1.
	switch s.Transport {
	case "":
		r.Errors = append(r.Errors, prefix+": transport is required (Phase 1: \"http\")")
	case "stdio":
		// Special-case message so operators get a clear roadmap signal
		// rather than a generic "unknown transport" error.
		r.Errors = append(r.Errors, fmt.Sprintf(
			"%s: transport \"stdio\" is not supported — stdio is on the roadmap; "+
				"Phase 1 supports HTTP transport only", prefix))
	default:
		if !knownMCPTransports[s.Transport] {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s: unknown transport %q (Phase 1: \"http\")", prefix, s.Transport))
		}
	}

	// URL: required for HTTP; must parse, must be http/https.
	if s.Transport == "http" || s.Transport == "" {
		if s.URL == "" {
			r.Errors = append(r.Errors, prefix+": url is required for http transport")
		} else if u, err := url.Parse(s.URL); err != nil || u.Host == "" {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s: url %q is malformed", prefix, s.URL))
		} else if u.Scheme != "http" && u.Scheme != "https" {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s: url %q must use http or https (got %q)", prefix, s.URL, u.Scheme))
		}
	}

	// Auth: when present, type must be known and required fields set.
	if s.Auth != nil {
		validateMCPAuth(prefix+".auth", *s.Auth, r)
	}

	// Tools: default-deny. Allow and Deny cannot both be empty —
	// operators must be explicit about exposure. (This is stricter
	// than the typical allowlist pattern by design.)
	validateMCPToolFilter(prefix+".tools", s.Tools, r)

	// Timeout: when set, must be ≥ 1s.
	if s.Timeout != 0 && s.Timeout < minMCPTimeout {
		r.Errors = append(r.Errors,
			fmt.Sprintf("%s: timeout %s is below minimum %s", prefix, s.Timeout, minMCPTimeout))
	}
}

func validateMCPAuth(prefix string, a types.MCPAuth, r *ValidationResult) {
	if a.Type == "" {
		r.Errors = append(r.Errors, prefix+": type is required")
		return
	}
	if !knownMCPAuthTypes[a.Type] {
		r.Errors = append(r.Errors, fmt.Sprintf(
			"%s: type %q must be one of: oauth, bearer, static", prefix, a.Type))
		return
	}

	switch a.Type {
	case "oauth":
		switch a.Grant {
		case "", "authorization_code":
			// #316: client_id + endpoints may be discovered (RFC 9728/8414/7591)
			// from the server url, so the trio is no longer required. Only a
			// PARTIAL endpoint config is an error — authorize_url and token_url
			// must be set together, or both omitted (⇒ discovery from url).
			if (a.AuthorizeURL == "") != (a.TokenURL == "") {
				r.Errors = append(r.Errors, prefix+": authorize_url and token_url must be set together (or both omitted to discover them from the server url)")
			}
		case "client_credentials":
			// #324: agent-principal 2LO — explicit client + secret + token
			// endpoint (no authorization endpoint, no DCR).
			if a.ClientID == "" {
				r.Errors = append(r.Errors, prefix+": client_id is required for grant client_credentials")
			}
			if a.ClientSecretEnv == "" {
				r.Errors = append(r.Errors, prefix+": client_secret_env is required for grant client_credentials")
			}
			if a.TokenURL == "" {
				r.Errors = append(r.Errors, prefix+": token_url is required for grant client_credentials")
			}
			if a.AuthorizeURL != "" {
				r.Errors = append(r.Errors, prefix+": authorize_url is not used with grant client_credentials")
			}
		default:
			r.Errors = append(r.Errors, fmt.Sprintf(
				"%s: grant %q must be one of: authorization_code, client_credentials", prefix, a.Grant))
		}
	case "bearer", "static":
		if a.TokenEnv == "" {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s: token_env is required for %s", prefix, a.Type))
		}
	}
}

func validateMCPToolFilter(prefix string, f types.MCPToolFilter, r *ValidationResult) {
	// Default-deny enforcement.
	if len(f.Allow) == 0 && len(f.Deny) == 0 {
		r.Errors = append(r.Errors, fmt.Sprintf(
			"%s: at least one of allow or deny must be set (default-deny — be explicit about tool exposure)",
			prefix))
		return
	}

	// Per-name format check. Also forbid "__" — that substring is
	// reserved for the runtime "<server>__<tool>" namespace
	// separator (review B9). An operator-supplied allow entry like
	// "foo__bar" would silently turn into "<server>__foo__bar" at
	// runtime, ambiguous to log parsers and a conflict vector.
	for i, name := range f.Allow {
		if name == "*" {
			continue // wildcard is allowed
		}
		if !mcpToolNamePattern.MatchString(name) {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s.allow[%d]: invalid tool name %q", prefix, i, name))
			continue
		}
		if strings.Contains(name, "__") {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s.allow[%d]: tool name %q contains \"__\" (reserved for MCP namespacing)", prefix, i, name))
		}
	}
	for i, name := range f.Deny {
		if !mcpToolNamePattern.MatchString(name) {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s.deny[%d]: invalid tool name %q", prefix, i, name))
			continue
		}
		if strings.Contains(name, "__") {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s.deny[%d]: tool name %q contains \"__\" (reserved for MCP namespacing)", prefix, i, name))
		}
	}

	// Cross-set overlap: a name in both Allow and Deny is a
	// programming bug, not a policy expression.
	allowSet := make(map[string]struct{}, len(f.Allow))
	for _, n := range f.Allow {
		allowSet[n] = struct{}{}
	}
	for _, n := range f.Deny {
		if _, dup := allowSet[n]; dup {
			r.Errors = append(r.Errors,
				fmt.Sprintf("%s: tool %q appears in both allow and deny", prefix, n))
		}
	}
}
