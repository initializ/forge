package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
)

// Gateway model-credential commands (#455). These are the MANUAL path for the
// apiKeyHelper gateway login when it is NOT driven by managed settings (a
// managed helper auto-logs-in via the login gate — see gateway_gate.go). They
// operate on the gateway credential (the short-lived token an api_key_helper
// mints), which is DISTINCT from `forge auth show-token`/`mint-token` (the
// internal A2A bearer token) and from the native provider OAuth credential that
// `forge init`/`forge try` sign into.

var authLoginCmd = &cobra.Command{
	Use:   "login [provider]",
	Short: "Log in to the model gateway via its configured api_key_helper",
	Long: `Run the api_key_helper configured in settings (models.gateway(s)) to
acquire and cache the gateway model credential. With multiple gateways
configured, pass the provider to disambiguate (e.g. 'forge auth login openai').

This is the manual counterpart to the managed login gate: when managed
settings configure the helper, LLM-touching commands log you in
automatically. Use this when the helper lives in your USER settings.

Laptop/dev command — refuses to run inside an agent runtime (a
container, or when FORGE_PLATFORM_TOKEN is set), which authenticates
with an injected key or platform token rather than an interactive login.`,
	Args:         cobra.MaximumNArgs(1),
	RunE:         runAuthLogin,
	SilenceUsage: true,
}

var authStatusCmd = &cobra.Command{
	Use:   "status [provider]",
	Short: "Show cached model-gateway credential status (never prints the token)",
	Long: `List the configured api_key_helper gateways and, for each, whether a
token is cached and when it expires. Prints metadata only — never the
token itself. Pass a provider to show just that gateway.`,
	Args:         cobra.MaximumNArgs(1),
	RunE:         runAuthStatus,
	SilenceUsage: true,
}

// gatewaysWithHelper returns the configured gateways that declare an
// api_key_helper. It resolves from TRUSTED layers only (excluding the
// checked-in project .forge/settings.json) — `forge auth login` execs the
// helper, so a cloned repo must not be able to inject the command. See PR #464
// review (HIGH #1).
func gatewaysWithHelper() ([]settings.ModelGateway, error) {
	layers, err := settings.LoadAllLayers(settings.LoadOptions{})
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}
	set := settings.Resolve(settings.TrustedGatewayLayers(layers))
	var out []settings.ModelGateway
	for _, gw := range set.Models.EffectiveGateways() {
		if strings.TrimSpace(gw.APIKeyHelper) != "" {
			out = append(out, gw)
		}
	}
	return out, nil
}

// gatewayProviderLabel is the human label for a gateway entry.
func gatewayProviderLabel(gw settings.ModelGateway) string {
	if gw.Provider == "" {
		return "(all providers)"
	}
	return gw.Provider
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	if reason, denied := deniedInAgentRuntime(); denied {
		return fmt.Errorf("refusing to run gateway login: %s. "+
			"`auth login` is a laptop/dev command; a deployed agent uses an injected "+
			"key or platform token", reason)
	}

	gws, err := gatewaysWithHelper()
	if err != nil {
		return err
	}
	if len(gws) == 0 {
		return fmt.Errorf("no api_key_helper configured in settings (set models.gateway.api_key_helper or a models.gateways entry)")
	}

	var chosen *settings.ModelGateway
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		provider := strings.ToLower(strings.TrimSpace(args[0]))
		for i := range gws {
			if strings.EqualFold(gws[i].Provider, provider) {
				chosen = &gws[i]
				break
			}
		}
		if chosen == nil {
			// Fall back to a provider-less catch-all if one is configured.
			for i := range gws {
				if gws[i].Provider == "" {
					chosen = &gws[i]
					break
				}
			}
		}
		if chosen == nil {
			return fmt.Errorf("no gateway with an api_key_helper for provider %q; configured: %s", provider, gatewayProviderList(gws))
		}
	} else if len(gws) == 1 {
		chosen = &gws[0]
	} else {
		return fmt.Errorf("multiple gateways configured — specify a provider: %s", gatewayProviderList(gws))
	}

	tok, err := runtime.EnsureGatewayToken(cmd.Context(), chosen.APIKeyHelper, chosen.Env)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "Logged in to gateway %s. %s\n", gatewayProviderLabel(*chosen), expiryPhrase(tok.ExpiresAt))
	return nil
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	gws, err := gatewaysWithHelper()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(gws) == 0 {
		_, _ = fmt.Fprintln(out, "No api_key_helper gateways configured in settings.")
		return nil
	}

	filter := ""
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		filter = strings.ToLower(strings.TrimSpace(args[0]))
	}

	shown := 0
	for i := range gws {
		gw := gws[i]
		if filter != "" && !strings.EqualFold(gw.Provider, filter) {
			continue
		}
		shown++
		tok, lerr := runtime.CachedGatewayToken(gw.APIKeyHelper)
		state := gatewayTokenState(tok, lerr)
		_, _ = fmt.Fprintf(out, "%s: %s\n", gatewayProviderLabel(gw), state)
	}
	if shown == 0 {
		_, _ = fmt.Fprintf(out, "No gateway with an api_key_helper for provider %q; configured: %s\n", filter, gatewayProviderList(gws))
	}
	return nil
}

// gatewayTokenState describes a cached token without ever revealing it.
func gatewayTokenState(tok *oauth.Token, lerr error) string {
	if lerr != nil {
		return "error reading cache"
	}
	if tok == nil || tok.AccessToken == "" {
		return "not logged in (run 'forge auth login')"
	}
	if tok.ExpiresAt.IsZero() {
		return "cached (opaque token — expiry unknown, re-fetched each run)"
	}
	if time.Now().After(tok.ExpiresAt) {
		return "expired at " + tok.ExpiresAt.Format(time.RFC1123)
	}
	return "valid until " + tok.ExpiresAt.Format(time.RFC1123)
}

func expiryPhrase(exp time.Time) string {
	if exp.IsZero() {
		return "Token cached (opaque — expiry unknown)."
	}
	return "Token valid until " + exp.Format(time.RFC1123) + "."
}

func gatewayProviderList(gws []settings.ModelGateway) string {
	labels := make([]string, 0, len(gws))
	for _, gw := range gws {
		labels = append(labels, gatewayProviderLabel(gw))
	}
	return strings.Join(labels, ", ")
}
