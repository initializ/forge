package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/spf13/cobra"
)

var (
	strict             bool
	commandCompat      bool
	platformPolicyPath string
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the agent spec and forge.yaml",
	Long: `Validate the agent spec and forge.yaml.

With --platform-policy=<file>, also lint a platform policy YAML file
(typically the source for the forge-platform-policy ConfigMap) without
needing a forge.yaml in scope. Used by operators / CI to gate policy
changes before kubectl apply. See docs/security/platform-policy.md.`,
	// Suppress cobra's usage dump after a RunE error — the validation
	// error message is the relevant output, not the command help.
	SilenceUsage: true,
	RunE:         runValidate,
}

func init() {
	validateCmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")
	validateCmd.Flags().BoolVar(&commandCompat, "command-compat", false, "check Command platform import compatibility")
	validateCmd.Flags().StringVar(&platformPolicyPath, "platform-policy", "", "validate a platform policy YAML file (standalone lint; no forge.yaml needed)")
}

func runValidate(cmd *cobra.Command, args []string) error {
	// Standalone platform-policy lint (issue #89 / FWS-5). Returns
	// non-zero on schema errors. Same UX shape as forge validate
	// forge.yaml — operators wire this into CI to gate ConfigMap
	// changes before kubectl apply.
	if platformPolicyPath != "" {
		if _, err := security.LoadPlatformPolicy(platformPolicyPath); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return fmt.Errorf("platform policy validation failed")
		}
		fmt.Println("Platform policy validation passed.")
		return nil
	}

	cfgPath := cfgFile
	if !filepath.IsAbs(cfgPath) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		cfgPath = filepath.Join(wd, cfgPath)
	}

	cfg, err := config.LoadForgeConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	result := validate.ValidateForgeConfig(cfg)

	// Also validate agent.json if it exists
	agentJSONPaths := []string{
		filepath.Join(filepath.Dir(cfgPath), ".forge-output", "agent.json"),
		filepath.Join(filepath.Dir(cfgPath), "agent.json"),
	}
	for _, p := range agentJSONPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		errs, err := validate.ValidateAgentSpec(data)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("agent.json schema error: %v", err))
			break
		}
		for _, e := range errs {
			result.Errors = append(result.Errors, fmt.Sprintf("agent.json: %s", e))
		}
		break
	}

	// Command compatibility check
	if commandCompat {
		agentJSONPath := filepath.Join(filepath.Dir(cfgPath), ".forge-output", "agent.json")
		agentData, err := os.ReadFile(agentJSONPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("command-compat: cannot read agent.json: %v (run 'forge build' first)", err))
		} else {
			var spec agentspec.AgentSpec
			if err := json.Unmarshal(agentData, &spec); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("command-compat: cannot parse agent.json: %v", err))
			} else {
				compatResult := validate.ValidateCommandCompat(&spec)
				for _, e := range compatResult.Errors {
					result.Errors = append(result.Errors, fmt.Sprintf("command-compat: %s", e))
				}
				for _, w := range compatResult.Warnings {
					result.Warnings = append(result.Warnings, fmt.Sprintf("command-compat: %s", w))
				}
			}
		}
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
	}

	if strict && len(result.Warnings) > 0 {
		return fmt.Errorf("validation failed: %d warning(s) treated as errors in strict mode", len(result.Warnings))
	}

	if !result.IsValid() {
		return fmt.Errorf("validation failed: %d error(s)", len(result.Errors))
	}

	fmt.Println("Validation passed.")
	return nil
}
