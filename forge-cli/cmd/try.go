package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-core/types"
	"github.com/spf13/cobra"
)

// forge try — the instant-gratification onboarding command. It scaffolds a
// keyless demo agent into a throwaway workspace, resolves whatever model
// credential is already available, and drops the user into a streaming chat
// whose tool calls and egress checks render inline. No build, no channels, no
// server, no secrets on disk. See issue #350.
var tryCmd = &cobra.Command{
	Use:   "try [prompt]",
	Short: "Talk to a live agent in your terminal — no build, no cluster",
	Long: "Scaffold a keyless demo agent and chat with it in your terminal, watching every " +
		"tool call and egress check as it runs. The workspace is ephemeral unless you pass " +
		"--keep, which writes it to ./forge-quickstart so you can make it your own.",
	Args: cobra.MaximumNArgs(1),
	RunE: runTry,
}

// tryFlags holds the parsed command flags for one `forge try` invocation.
type tryFlags struct {
	keep     bool
	provider string
	model    string
	once     string
	onceSet  bool // whether --once was explicitly set (distinguishes "" from unset)
	quiet    bool
	audit    bool
	yes      bool
	prompt   string // optional positional first prompt
}

func init() {
	tryCmd.Flags().Bool("keep", false, "keep the demo agent in ./forge-quickstart instead of a temp dir")
	tryCmd.Flags().String("provider", "", "model provider: openai, anthropic, gemini, or ollama")
	tryCmd.Flags().String("model", "", "model name (defaults to the provider's default)")
	tryCmd.Flags().String("once", "", "run a single prompt non-interactively, then exit")
	tryCmd.Flags().Bool("quiet", false, "hide the inline tool/egress loop lines")
	tryCmd.Flags().Bool("audit", false, "show the full NDJSON audit event stream")
	tryCmd.Flags().Bool("yes", false, "assume yes to prompts (non-interactive)")
}

func parseTryFlags(cmd *cobra.Command, args []string) tryFlags {
	f := tryFlags{}
	f.keep, _ = cmd.Flags().GetBool("keep")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.once, _ = cmd.Flags().GetString("once")
	f.onceSet = cmd.Flags().Changed("once")
	f.quiet, _ = cmd.Flags().GetBool("quiet")
	f.audit, _ = cmd.Flags().GetBool("audit")
	f.yes, _ = cmd.Flags().GetBool("yes")
	if len(args) > 0 {
		f.prompt = args[0]
	}
	return f
}

func runTry(cmd *cobra.Command, args []string) error {
	flags := parseTryFlags(cmd, args)

	// Phase 2 replaces this with resolveTryProvider (env key → saved OAuth →
	// Ollama → interactive picker). For now the provider is explicit.
	if flags.provider == "" {
		return fmt.Errorf("specify --provider (openai, anthropic, gemini, or ollama); " +
			"automatic credential resolution lands in a later build")
	}

	dir, cleanup, err := tryWorkspace(flags.keep)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	opts := quickstartPreset(flags.provider, flags.model)
	opts.OutputDir = dir
	opts.Force = true // forge owns this dir (a temp dir, or ./forge-quickstart)

	if err := scaffold(opts); err != nil {
		return err
	}

	cfg, err := config.LoadForgeConfig(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		return fmt.Errorf("loading scaffolded agent: %w", err)
	}

	printTrySummary(cmd, cfg, opts)

	// Phase 3 drives the REPL / --once turn here. Phase 1 stops after the
	// agent loads and its summary prints.
	if flags.keep {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n  Kept the demo agent in ./forge-quickstart\n")
	}
	return nil
}

// tryWorkspace returns the scaffold target directory, plus a cleanup func for
// the ephemeral case (nil under --keep). The ephemeral agent lives in a leaf
// under a fresh temp dir so scaffold()'s not-exists check still holds, and the
// whole temp tree is removed on exit — no secrets ever persist.
func tryWorkspace(keep bool) (dir string, cleanup func(), err error) {
	if keep {
		return filepath.Join(".", "forge-quickstart"), nil, nil
	}
	base, err := os.MkdirTemp("", "forge-try-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp workspace: %w", err)
	}
	return filepath.Join(base, "quickstart"), func() { _ = os.RemoveAll(base) }, nil
}

// quickstartPreset is the demo agent: the native forge LLM executor, the
// keyless weather skill, and a few safe builtins. Egress is auto-derived from
// the skill (allowlist, no dev-open); no channels, no long-term memory, no
// secrets. Preset stops scaffold() before its auto-run so `forge try` drives
// the loop in-process.
func quickstartPreset(provider, model string) *initOptions {
	return &initOptions{
		Name:           "quickstart",
		AgentID:        "quickstart",
		Framework:      "forge",
		ModelProvider:  provider,
		CustomModel:    model, // empty → provider default (buildTemplateData)
		AuthMethod:     "apikey",
		BuiltinTools:   []string{"http_request", "datetime_now", "math_calculate"},
		Skills:         []string{"weather"},
		NonInteractive: true,
		Preset:         true,
		EnvVars:        map[string]string{},
	}
}

// printTrySummary prints the one-line agent summary shown at startup, e.g.
//
//	Agent: quickstart · skills: weather · tools: http_request, datetime_now, math_calculate
func printTrySummary(cmd *cobra.Command, cfg *types.ForgeConfig, opts *initOptions) {
	out := cmd.OutOrStdout()
	parts := []string{fmt.Sprintf("Agent: %s", cfg.AgentID)}
	if len(opts.Skills) > 0 {
		parts = append(parts, "skills: "+strings.Join(opts.Skills, ", "))
	}
	if len(opts.BuiltinTools) > 0 {
		parts = append(parts, "tools: "+strings.Join(opts.BuiltinTools, ", "))
	}
	_, _ = fmt.Fprintf(out, "  %s\n", strings.Join(parts, " · "))
}
