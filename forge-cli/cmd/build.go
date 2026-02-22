package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-cli/build"
	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/plugins/crewai"
	"github.com/initializ/forge/forge-cli/plugins/custom"
	"github.com/initializ/forge/forge-cli/plugins/langchain"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the agent container artifact",
	RunE:  runBuild,
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

	outDir := outputDir
	if outDir == "." {
		outDir = filepath.Join(filepath.Dir(cfgPath), ".forge-output")
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:    filepath.Dir(cfgPath),
		OutputDir:  outDir,
		ConfigPath: cfgPath,
	})
	bc.Config = cfg
	bc.Verbose = verbose

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
		&build.RequirementsStage{},
		&build.PolicyStage{},
		&build.EgressStage{},
		&build.DockerfileStage{},
		&build.K8sStage{},
		&build.ValidateStage{},
		&build.ManifestStage{},
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
