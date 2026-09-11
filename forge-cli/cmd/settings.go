package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/settings"
)

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Inspect forge settings (developer surface: enabled channels, models, tools, gateway)",
	Long: `Show the effective forge settings and the layers they were resolved from.

Settings are the developer-surface configuration (issue #454): which channels
are enabled, the default model + gateway endpoint, and which builtin tools are
offered. They layer, highest precedence first:

  1. Managed    (managed-settings.json in the OS system dir + managed-settings.d/, or FORGE_MANAGED_SETTINGS)
  2. CLI        (--settings <file>)
  3. Project    (.forge/settings.local.json, then .forge/settings.json)
  4. User       (~/.forge/settings.json)

Managed settings sit at the top and win over lower layers; a managed
models.available_models is the authoritative allowlist among the settings
layers (an org default). Settings are the developer surface, not a tamper-proof
control — the non-overridable deny/forbidden-model enforcement is platform
policy (forge-core/security), injected server-side by the control plane.`,
	RunE: settingsShowRun,
}

func init() {
	settingsCmd.AddCommand(settingsShowCmd)
	settingsShowCmd.Flags().String("settings", "", "additional settings file to layer (CLI precedence)")
	settingsShowCmd.Flags().Bool("json", false, "emit the resolved settings as JSON")
	// `forge settings` with no subcommand behaves like `forge settings show`.
	settingsCmd.RunE = settingsShowRun
	settingsCmd.Flags().String("settings", "", "additional settings file to layer (CLI precedence)")
	settingsCmd.Flags().Bool("json", false, "emit the resolved settings as JSON")
}

var settingsShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the effective settings and their source layers",
	RunE:  settingsShowRun,
}

func settingsShowRun(cmd *cobra.Command, _ []string) error {
	cliPath, _ := cmd.Flags().GetString("settings")
	asJSON, _ := cmd.Flags().GetBool("json")

	layers, err := settings.LoadAllLayers(settings.LoadOptions{CLISettingsPath: cliPath})
	if err != nil {
		return err
	}
	effective := settings.Resolve(layers)

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(effective)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Loaded layers (lowest → highest precedence):")
	if len(layers) == 0 {
		_, _ = fmt.Fprintln(out, "  (none — using built-in defaults)")
	}
	for _, l := range layers {
		lock := ""
		if l.ManagedLock {
			lock = "  [available_models LOCK]"
		}
		_, _ = fmt.Fprintf(out, "  %-14s %s%s\n", l.Source, l.Path, lock)
	}

	_, _ = fmt.Fprintln(out, "\nEffective settings:")
	// Mask env VALUES in the human view — env is the field most likely to carry
	// a secret. Keys are shown so operators see what's set. The raw values are
	// available via --json for the operator's own machine-readable dump (which
	// prints verbatim — don't paste that into shared channels).
	display := effective
	display.Env = maskEnvValues(effective.Env)
	b, err := json.MarshalIndent(display, "  ", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "  %s\n", b)
	return nil
}

// maskEnvValues returns a copy of env with each value replaced by "***" so the
// human `forge settings show` output never prints a value that might be a
// secret. Returns nil for empty input (keeps the field omitted).
func maskEnvValues(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	masked := make(map[string]string, len(env))
	for k := range env {
		masked[k] = "***"
	}
	return masked
}
