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
	default:
		return nil, nil, fmt.Errorf("unknown --auth value %q (supported: none, oidc, http_verifier, custom)", mode)
	}
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
//	         name: <name>
//	         settings:
//	           key: value
//	           nested:
//	             k: v
//
// Settings keys are emitted in alphabetical order so the output is
// deterministic across runs (useful for diffing generated files).
func renderAuthBlock(mode, name string, settings map[string]any) string {
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
	if name != "" && name != mode {
		fmt.Fprintf(&b, "      name: %s\n", name)
	}
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
		case string:
			fmt.Fprintf(b, "%s%s: %s\n", indent, k, val)
		default:
			fmt.Fprintf(b, "%s%s: %v\n", indent, k, val)
		}
	}
}
