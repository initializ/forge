package cmd

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// buildAuthFromFlags reads the --auth* flag set into a settings map +
// the egress hosts the runtime will need. Validates required-field
// combinations; rejects unknown modes with a clear error.
func buildAuthFromFlags(cmd *cobra.Command, mode string) (settings map[string]any, egressHosts []string, err error) {
	switch mode {
	case "none", "custom":
		return nil, nil, nil
	case "oidc":
		issuer, _ := cmd.Flags().GetString("auth-issuer")
		audience, _ := cmd.Flags().GetString("auth-audience")
		groupsClaim, _ := cmd.Flags().GetString("auth-groups-claim")
		if issuer == "" || audience == "" {
			return nil, nil, fmt.Errorf("--auth=oidc requires --auth-issuer and --auth-audience")
		}
		issuer = strings.TrimRight(issuer, "/")
		settings = map[string]any{
			"issuer":   issuer,
			"audience": audience,
		}
		if groupsClaim != "" && groupsClaim != "groups" {
			settings["claim_map"] = map[string]any{"groups": groupsClaim}
		}
		if host := hostFromURL(issuer); host != "" {
			egressHosts = []string{host}
		}
		return settings, egressHosts, nil
	case "http_verifier":
		verifierURL, _ := cmd.Flags().GetString("auth-url")
		defaultOrg, _ := cmd.Flags().GetString("auth-default-org")
		if verifierURL == "" {
			return nil, nil, fmt.Errorf("--auth=http_verifier requires --auth-url")
		}
		settings = map[string]any{"url": verifierURL}
		if defaultOrg != "" {
			settings["default_org"] = defaultOrg
		}
		if host := hostFromURL(verifierURL); host != "" {
			egressHosts = []string{host}
		}
		return settings, egressHosts, nil
	case "aws_sigv4":
		region, _ := cmd.Flags().GetString("auth-aws-region")
		audience, _ := cmd.Flags().GetString("auth-aws-audience")
		allowedPrincipals, _ := cmd.Flags().GetStringSlice("auth-aws-allowed-principal")
		allowedAccounts, _ := cmd.Flags().GetStringSlice("auth-aws-allowed-account")
		cacheTTL, _ := cmd.Flags().GetString("auth-aws-cache-ttl")
		if region == "" {
			return nil, nil, fmt.Errorf("--auth=aws_sigv4 requires --auth-aws-region")
		}
		settings = map[string]any{"region": region}
		if audience != "" {
			settings["audience"] = audience
		}
		if len(allowedPrincipals) > 0 {
			settings["allowed_principals"] = allowedPrincipals
		}
		if len(allowedAccounts) > 0 {
			settings["allowed_accounts"] = allowedAccounts
		}
		if cacheTTL != "" {
			settings["identity_cache_ttl"] = cacheTTL
		}
		egressHosts = []string{"sts." + region + ".amazonaws.com"}
		return settings, egressHosts, nil
	case "gcp_iap":
		audience, _ := cmd.Flags().GetString("auth-gcp-iap-audience")
		if audience == "" {
			return nil, nil, fmt.Errorf("--auth=gcp_iap requires --auth-gcp-iap-audience")
		}
		settings = map[string]any{"audience": audience}
		// IAP JWKS host is hardcoded (decision §9.4).
		egressHosts = []string{"www.gstatic.com"}
		return settings, egressHosts, nil
	case "azure_ad":
		tenant, _ := cmd.Flags().GetString("auth-azure-tenant")
		audience, _ := cmd.Flags().GetString("auth-azure-audience")
		multiTenant, _ := cmd.Flags().GetBool("auth-azure-multi-tenant")
		allowedTenants, _ := cmd.Flags().GetStringSlice("auth-azure-allowed-tenant")
		groupsMode, _ := cmd.Flags().GetString("auth-azure-groups-mode")
		if audience == "" {
			return nil, nil, fmt.Errorf("--auth=azure_ad requires --auth-azure-audience")
		}
		if !multiTenant && tenant == "" {
			return nil, nil, fmt.Errorf("--auth=azure_ad requires --auth-azure-tenant unless --auth-azure-multi-tenant=true")
		}
		if !multiTenant && len(allowedTenants) > 0 {
			return nil, nil, fmt.Errorf("--auth-azure-allowed-tenant is only meaningful with --auth-azure-multi-tenant=true")
		}
		settings = map[string]any{"audience": audience}
		if tenant != "" {
			settings["tenant_id"] = tenant
		}
		if multiTenant {
			settings["allow_multi_tenant"] = true
		}
		if len(allowedTenants) > 0 {
			settings["allowed_tenants"] = allowedTenants
		}
		if groupsMode != "" {
			settings["groups_mode"] = groupsMode
		}
		egressHosts = []string{"login.microsoftonline.com"}
		if groupsMode == "graph" {
			egressHosts = append(egressHosts, "graph.microsoft.com")
		}
		return settings, egressHosts, nil
	default:
		return nil, nil, fmt.Errorf("unknown --auth value %q (supported: none, oidc, http_verifier, aws_sigv4, gcp_iap, azure_ad, custom)", mode)
	}
}

// authEgressHostsFromSettings extracts the outbound hosts implied by an
// auth provider's settings block. Used by the Web UI path (cmd/ui.go) to
// derive the same egress hosts the TUI wizard / --auth flags compute.
//
// Returns nil for modes that don't require outbound (none, static_token,
// custom) or for malformed/missing URLs.
func authEgressHostsFromSettings(mode string, settings map[string]any) []string {
	if settings == nil {
		return nil
	}
	var hosts []string
	switch mode {
	case "oidc":
		if h := hostFromURL(asStringSetting(settings, "issuer")); h != "" {
			hosts = append(hosts, h)
		}
		if h := hostFromURL(asStringSetting(settings, "jwks_url")); h != "" {
			hosts = append(hosts, h)
		}
	case "http_verifier":
		if h := hostFromURL(asStringSetting(settings, "url")); h != "" {
			hosts = append(hosts, h)
		}
	case "aws_sigv4":
		region := asStringSetting(settings, "region")
		if region != "" {
			hosts = append(hosts, "sts."+region+".amazonaws.com")
		}
	case "gcp_iap":
		// Decision §9.4: hardcoded JWKS host.
		hosts = append(hosts, "www.gstatic.com")
	case "azure_ad":
		hosts = append(hosts, "login.microsoftonline.com")
		if asStringSetting(settings, "groups_mode") == "graph" {
			hosts = append(hosts, "graph.microsoft.com")
		}
	}
	return hosts
}

// asStringSetting reads a string-valued setting, returning "" for missing
// or non-string values.
func asStringSetting(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// hostFromURL extracts the bare host (no port) from a URL string. Returns
// "" if the URL is malformed — validation happens in the wizard /
// buildAuthFromFlags above.
func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// renderAuthBlock returns a forge.yaml `auth:` fragment for the given
// provider type and settings. The output starts at column 0 with no
// trailing newline so the surrounding template controls spacing.
//
// Three shapes are produced:
//
//	mode == ""      → empty string (skip)
//	mode == "none"  → empty string (anonymous)
//	mode == "custom"
//	   ↓
//	   # Auth provider chain — configure here.
//	   # auth:
//	   #   required: true
//	   #   providers: [...]
//
//	mode == "oidc" | "http_verifier" | "static_token"
//	   ↓
//	   auth:
//	     required: true
//	     providers:
//	       - type: <mode>
//	         settings:
//	           key: value
//	           nested:
//	             k: v
//
// The AuthProvider.Name field is intentionally NOT emitted — the wizard
// doesn't capture one, and the previous signature took a `name` arg
// that callers always set equal to mode (suppressed). Removed in
// review #11d. If a future wizard step adds explicit name capture,
// reintroduce the parameter then.
//
// Settings keys are emitted in alphabetical order so the output is
// deterministic across runs (useful for diffing generated files).
func renderAuthBlock(mode string, settings map[string]any) string {
	switch mode {
	case "", "none":
		return ""
	case "custom":
		return customAuthStub()
	}

	var b strings.Builder
	b.WriteString("auth:\n")
	b.WriteString("  required: true\n")
	b.WriteString("  providers:\n")
	fmt.Fprintf(&b, "    - type: %s\n", mode)
	if len(settings) > 0 {
		b.WriteString("      settings:\n")
		writeYAMLMap(&b, settings, "        ")
	}
	// Trim the final newline so the template controls spacing.
	return strings.TrimRight(b.String(), "\n")
}

// customAuthStub returns the commented-out template a user gets when they
// pick "Custom" in the wizard.
func customAuthStub() string {
	return strings.Join([]string{
		"# Auth provider chain — configure here. See useforge.ai/docs/auth",
		"# Supported types: oidc, http_verifier, static_token",
		"# auth:",
		"#   required: true",
		"#   providers:",
		"#     - type: oidc",
		"#       settings:",
		"#         issuer: https://login.example.com",
		"#         audience: api://forge",
	}, "\n")
}

// writeYAMLMap renders a `map[string]any` as YAML lines, recursing into
// nested maps. Only string / number / bool / map values are supported —
// the auth-settings schema doesn't use anything else.
//
// String values are conservatively quoted (review #12.8) when they
// contain YAML-significant characters. Otherwise the unquoted form is
// emitted to keep generated forge.yaml readable.
func writeYAMLMap(b *strings.Builder, m map[string]any, indent string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		switch val := v.(type) {
		case map[string]any:
			fmt.Fprintf(b, "%s%s:\n", indent, k)
			writeYAMLMap(b, val, indent+"  ")
		case []string:
			// Phase 2: aws_sigv4's allowed_principals is the only []string
			// currently in the auth settings schema. ARNs frequently contain
			// `:` so each entry goes through yamlScalar for proper quoting.
			fmt.Fprintf(b, "%s%s:\n", indent, k)
			for _, item := range val {
				fmt.Fprintf(b, "%s  - %s\n", indent, yamlScalar(item))
			}
		case []any:
			// Defensive: when settings come from YAML unmarshal, lists land
			// as []any. Coerce per-element and reuse the string path.
			fmt.Fprintf(b, "%s%s:\n", indent, k)
			for _, item := range val {
				if s, ok := item.(string); ok {
					fmt.Fprintf(b, "%s  - %s\n", indent, yamlScalar(s))
				} else {
					fmt.Fprintf(b, "%s  - %v\n", indent, item)
				}
			}
		case string:
			fmt.Fprintf(b, "%s%s: %s\n", indent, k, yamlScalar(val))
		default:
			fmt.Fprintf(b, "%s%s: %v\n", indent, k, val)
		}
	}
}

// yamlScalar returns a YAML-safe rendering of a string value. Plain
// values pass through unchanged; values containing characters that
// would break the YAML parser (": " sequences, leading specials,
// reserved tokens, control chars) are emitted as double-quoted strings
// with the minimum necessary escaping.
//
// This is deliberately not a general YAML serializer — it covers the
// subset that appears in auth provider settings (issuer URLs, audiences,
// claim names, default org strings). If we ever need richer values,
// switch to gopkg.in/yaml.v3 for the whole block.
func yamlScalar(s string) string {
	if !needsYAMLQuoting(s) {
		return s
	}
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// needsYAMLQuoting reports whether a string would change meaning when
// emitted unquoted in a YAML block-scalar context. Conservative —
// false positives are fine (extra quotes), false negatives are bugs
// (broken YAML).
func needsYAMLQuoting(s string) bool {
	if s == "" {
		return true
	}
	// Leading characters that begin YAML indicators (tags, anchors,
	// aliases, folded/literal block markers, flow markers, directives,
	// quotes, comments, leading whitespace).
	switch s[0] {
	case '!', '&', '*', '>', '|', '%', '@', '`', '"', '\'', '#', ' ', '\t', '[', ']', '{', '}', ',', '?', ':', '-':
		return true
	}
	// Trailing colon (key-like form) or any unquoted ": " (mapping
	// indicator inside a scalar would split into key/value).
	if strings.HasSuffix(s, ":") || strings.Contains(s, ": ") || strings.Contains(s, " #") {
		return true
	}
	// Control / newline characters.
	if strings.ContainsAny(s, "\n\r\t\v\f") {
		return true
	}
	// YAML 1.1 boolean / null literals — unquoted they decode to bool/nil.
	// We keep this case-insensitive to match yaml.v3 defaults.
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	// All-digit strings would otherwise decode as integers — quote them
	// to preserve string semantics (e.g. AWS account IDs are 12-digit
	// strings, NOT numbers — and the provider expects []string).
	if isAllDigits(s) {
		return true
	}
	return false
}

// isAllDigits returns true if s is non-empty and consists only of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
