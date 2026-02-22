package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/compiler"
	"github.com/initializ/forge/forge-core/pipeline"
)

// AgentSpecStage generates agent.json from ForgeConfig.
type AgentSpecStage struct{}

func (s *AgentSpecStage) Name() string { return "generate-agentspec" }

func (s *AgentSpecStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	spec := compiler.ConfigToAgentSpec(bc.Config)

	if bc.PluginConfig != nil {
		compiler.MergePluginConfig(spec, bc.PluginConfig)
	}
	if bc.WrapperFile != "" {
		spec.Runtime.Entrypoint = compiler.WrapperEntrypoint(bc.WrapperFile)
	}

	bc.Spec = spec

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling agent spec: %w", err)
	}

	outPath := filepath.Join(bc.Opts.OutputDir, "agent.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing agent.json: %w", err)
	}

	bc.AddFile("agent.json", outPath)
	return nil
}
