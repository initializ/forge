package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-cli/runtime"
)

// guardrailsCmd groups the operator-facing guardrails helpers. Guardrails
// run from the agent's guardrails.json (falling back to the built-in
// DefaultStructuredGuardrails when the file is absent), optionally tightened
// by the platform guardrails overlay in policy.yaml (#284). See
// docs/security/guardrails.md.
var guardrailsCmd = &cobra.Command{
	Use:   "guardrails",
	Short: "Manage guardrails configuration",
	Long: `Operator helpers for the agent's guardrails.

Guardrails are read from guardrails.json in the agent's workdir; when the
file is absent the built-in DefaultStructuredGuardrails apply. A platform
operator can further RESTRICT them (never loosen) via the guardrails: overlay
in the platform policy.yaml — see docs/security/platform-policy.md and #284.

See docs/security/guardrails.md for the full resolution ladder.`,
}

var guardrailsSeedDefaultsCmd = &cobra.Command{
	Use:   "seed-defaults",
	Short: "Print DefaultStructuredGuardrails as JSON to scaffold a guardrails.json",
	Long: `Print the built-in DefaultStructuredGuardrails as pretty-printed JSON.

The output matches the guardrails.json schema (models.StructuredGuardrails)
and is a ready baseline to drop into an agent's workdir. Pipe to a file and
edit before use if you want to tweak thresholds or add rules.

  forge guardrails seed-defaults > guardrails.json

The built-in defaults cover 11 vendor-secret patterns (Anthropic / OpenAI /
GitHub / AWS / Slack / Telegram tokens, private-key PEM blocks), four PII
categories (email / phone / SSN / credit card), and jailbreak /
prompt-injection / command-injection thresholds.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		sg := runtime.DefaultStructuredGuardrails()
		out, err := json.MarshalIndent(sg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling defaults: %w", err)
		}
		// Trailing newline so shell redirection produces a well-formed
		// file. cobra writes to OutOrStdout so tests can capture stdout.
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	},
}

func init() {
	guardrailsCmd.AddCommand(guardrailsSeedDefaultsCmd)
}
