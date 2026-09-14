package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/settings"
)

// The gateway login gate (#455). When MANAGED settings configure a model
// gateway with an api_key_helper, a command that is about to make LLM calls
// (re)acquires the gateway token before proceeding — so a developer on a
// managed laptop is transparently logged in. Only a MANAGED helper arms this;
// a user-layer helper stays on the manual `forge auth login|logout|status`
// path. Nothing here fetches for non-LLM commands, and a settings-load error is
// non-fatal (the `forge settings` command is where such errors surface loudly).
func init() {
	rootCmd.PersistentPreRunE = gatewayLoginGate
}

func gatewayLoginGate(cmd *cobra.Command, _ []string) error {
	if !gatewayGateApplies(cmd) {
		return nil
	}
	layers, err := settings.LoadAllLayers(settings.LoadOptions{})
	if err != nil {
		return nil // don't hard-block on a settings parse error; overlay re-surfaces it
	}
	managed := settings.ManagedLayer(layers)
	if managed == nil {
		return nil // only a managed helper arms the gate
	}
	// A MANAGED helper is what ARMS the gate — but the credential's (helper, env)
	// identity must be resolved from the same TRUSTED-MERGED set the runtime
	// overlay uses to LOOK UP the token, or the gate would mint+cache under a key
	// the overlay never reads. They can differ only for the singular catch-all,
	// whose env merges additively across trusted layers (per-provider entries are
	// whole-entry replaced, managed winning). Managed always wins the helper, so a
	// lower trusted layer can only contribute env, never redirect the command.
	merged := settings.Resolve(settings.TrustedGatewayLayers(layers))

	// Dedup on the (helper, env) IDENTITY, not the helper alone: a shared helper
	// parameterized with different env per provider (e.g. per-provider
	// OKTA_ISSUER) mints distinct tokens and must each be fetched.
	seen := make(map[string]bool)
	for _, mgw := range managed.Settings.Models.EffectiveGateways() {
		if strings.TrimSpace(mgw.APIKeyHelper) == "" {
			continue // only a managed helper arms the gate
		}
		// Resolve the effective gateway (same helper + env the overlay will look
		// up) for this provider from the trusted-merged set.
		eff := merged.Models.GatewayForProvider(mgw.Provider)
		if eff == nil || strings.TrimSpace(eff.APIKeyHelper) == "" {
			continue
		}
		id := runtime.GatewayCredKey(eff.APIKeyHelper, eff.Env)
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := runtime.EnsureGatewayToken(cmd.Context(), eff.APIKeyHelper, eff.Env); err != nil {
			return fmt.Errorf("gateway login failed (run 'forge auth login'): %w", err)
		}
	}
	return nil
}

// gatewayGateApplies reports whether cmd is an LLM-touching command the gate
// should cover: `run`, `try`, and the server-start path (bare `serve` or
// `serve start`). Management verbs (serve stop/status/logs, tool list/describe,
// etc.) build no model client and are never gated.
func gatewayGateApplies(cmd *cobra.Command) bool {
	top := cmd
	for top.Parent() != nil && top.Parent().Parent() != nil {
		top = top.Parent()
	}
	switch top.Name() {
	case "run", "try":
		return true
	case "serve":
		leaf := cmd.Name()
		return leaf == "serve" || leaf == "start"
	}
	return false
}
