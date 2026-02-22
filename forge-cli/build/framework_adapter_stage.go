package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/plugins"
)

// FrameworkAdapterStage detects the agent framework, extracts configuration,
// and generates an A2A wrapper if needed.
type FrameworkAdapterStage struct {
	Registry *plugins.FrameworkRegistry
}

func (s *FrameworkAdapterStage) Name() string { return "framework-adapter" }

func (s *FrameworkAdapterStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if s.Registry == nil {
		return nil
	}

	// Resolve plugin: explicit framework config or auto-detect
	var plugin plugins.FrameworkPlugin
	if bc.Config.Framework != "" {
		plugin = s.Registry.Get(bc.Config.Framework)
	}
	if plugin == nil {
		var err error
		plugin, err = s.Registry.Detect(bc.Opts.WorkDir)
		if err != nil {
			return fmt.Errorf("detecting framework: %w", err)
		}
	}

	if plugin == nil {
		return nil // no framework detected, skip silently
	}

	// Extract agent config from source code
	agentConfig, err := plugin.ExtractAgentConfig(bc.Opts.WorkDir)
	if err != nil {
		return fmt.Errorf("extracting agent config (%s): %w", plugin.Name(), err)
	}
	bc.PluginConfig = agentConfig

	// Generate wrapper
	wrapperData, err := plugin.GenerateWrapper(agentConfig)
	if err != nil {
		return fmt.Errorf("generating wrapper (%s): %w", plugin.Name(), err)
	}

	if wrapperData != nil {
		wrapperName := "a2a_wrapper.py"
		wrapperPath := filepath.Join(bc.Opts.OutputDir, wrapperName)
		if err := os.WriteFile(wrapperPath, wrapperData, 0644); err != nil {
			return fmt.Errorf("writing wrapper: %w", err)
		}
		bc.WrapperFile = wrapperName
		bc.AddFile(wrapperName, wrapperPath)
	}

	// Log runtime dependencies as warnings
	if bc.Verbose {
		for _, dep := range plugin.RuntimeDependencies() {
			bc.AddWarning(fmt.Sprintf("framework runtime dependency: %s", dep))
		}
	}

	return nil
}
