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

	// Ensure each managed gateway's credential. Dedup on the (helper, env)
	// IDENTITY, not the helper alone: a shared helper parameterized with
	// different env per provider (e.g. per-provider OKTA_ISSUER) mints distinct
	// tokens and must each be fetched. runtime.GatewayCredKey gives the same
	// (helper, env) identity the credential cache uses.
	seen := make(map[string]bool)
	for _, gw := range managed.Settings.Models.EffectiveGateways() {
		h := strings.TrimSpace(gw.APIKeyHelper)
		if h == "" {
			continue
		}
		id := runtime.GatewayCredKey(h, gw.Env)
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := runtime.EnsureGatewayToken(cmd.Context(), h, gw.Env); err != nil {
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
