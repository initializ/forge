package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-cli/build"
	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/plugins/crewai"
	"github.com/initializ/forge/forge-cli/plugins/custom"
	"github.com/initializ/forge/forge-cli/plugins/langchain"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/spf13/cobra"
)

var (
	signingKey      string
	buildSlim       bool
	buildAlpine     bool
	localBins       []string
	buildPolicyPath string
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the agent container artifact",
	RunE:  runBuild,
}

func init() {
	buildCmd.Flags().StringVar(&signingKey, "signing-key", "", "path to Ed25519 private key for signing build output")
	buildCmd.Flags().BoolVar(&buildSlim, "slim", false, "minimize image size (skip heavy/optional binaries)")
	buildCmd.Flags().BoolVar(&buildAlpine, "alpine", false, "prefer Alpine base image")
	buildCmd.Flags().StringArrayVar(&localBins, "local-bin", nil, "local binary override as name=/path/to/file (repeatable)")
	buildCmd.Flags().StringVar(&buildPolicyPath, "policy", "", "Path to a YAML security policy file (overrides forge.yaml security.policy_path and the builtin DefaultPolicy)")

}

func runBuild(cmd *cobra.Command, args []string) error {
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

	// Pre-validate config
	result := validate.ValidateForgeConfig(cfg)
	if !result.IsValid() {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
		}
		return fmt.Errorf("config validation failed: %d error(s)", len(result.Errors))
	}

	// Parse --local-bin flags and merge into config
	parsedLocalBins, err := parseLocalBins(localBins)
	if err != nil {
		return err
	}
	if len(parsedLocalBins) > 0 {
		if cfg.Package.BinOverrides == nil {
			cfg.Package.BinOverrides = make(map[string]types.BinOverride)
		}
		for name, path := range parsedLocalBins {
			override := cfg.Package.BinOverrides[name]
			override.LocalPath = path
			cfg.Package.BinOverrides[name] = override
		}
	}

	outDir := outputDir
	if outDir == "." {
		outDir = filepath.Join(filepath.Dir(cfgPath), ".forge-output")
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:        filepath.Dir(cfgPath),
		OutputDir:      outDir,
		ConfigPath:     cfgPath,
		SigningKeyPath: signingKey,
	})
	bc.Config = cfg
	bc.Verbose = verbose
	bc.LocalBins = parsedLocalBins
	bc.PreferAlpine = buildAlpine || cfg.Package.Alpine
	bc.PreferSlim = buildSlim || cfg.Package.Slim
	bc.ForgeCLIVersion = appVersion

	reg := plugins.NewFrameworkRegistry()
	reg.Register(&crewai.Plugin{})
	reg.Register(&langchain.Plugin{})
	reg.Register(&custom.Plugin{})

	p := pipeline.New(
		&build.FrameworkAdapterStage{Registry: reg},
		&build.AgentSpecStage{},
		&build.ToolsStage{},
		&build.ToolFilterStage{},
		&build.SkillsStage{},
		&build.SecurityAnalysisStage{PolicyPathOverride: buildPolicyPath},
		&build.RequirementsStage{},
		&build.ChannelsStage{},
		&build.PolicyStage{},
		&build.EgressStage{},
		&build.DockerfileStage{},
		&build.SecretSafetyStage{},
		&build.K8sStage{},
		&build.ValidateStage{},
		&build.ManifestStage{},
		&build.SigningStage{},
	)

	if err := p.Run(context.Background(), bc); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	for _, w := range bc.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	fmt.Printf("Build complete. Output: %s\n", outDir)
	return nil
}

// parseLocalBins parses "name=/path/to/file" pairs and validates file existence.
func parseLocalBins(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(args))
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid --local-bin format %q: expected name=/path/to/file", arg)
		}
		name, path := parts[0], parts[1]
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolving path for --local-bin %s: %w", name, err)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("--local-bin %s: file %q not found: %w", name, absPath, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("--local-bin %s: %q is a directory, expected a file", name, absPath)
		}
		result[name] = absPath
	}
	return result, nil
}
