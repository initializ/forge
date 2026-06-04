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

	// Populate spec.A2A.Skills from SKILL.md frontmatter so the published
	// AgentSpec advertises the agent's skill surface (issue #85). Without
	// this, consumers of agent.json (and the runner's AgentCardFromSpec
	// path post-build) would only see builtin tools — the SKILL.md files
	// would never reach the A2A Agent Card.
	populateA2ASkillsFromSKILLmd(spec, bc.Opts.WorkDir, bc.Config.Skills.Path)

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
