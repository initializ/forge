package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/security"
)

// EgressStage resolves egress configuration and generates allowlist artifacts.
type EgressStage struct{}

func (s *EgressStage) Name() string { return "resolve-egress" }

func (s *EgressStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	cfg := bc.Config.Egress

	// No-op if no egress config
	if cfg.Profile == "" && cfg.Mode == "" {
		return nil
	}

	// Collect tool names for domain inference
	var toolNames []string
	if bc.Spec != nil {
		for _, t := range bc.Spec.Tools {
			toolNames = append(toolNames, t.Name)
		}
	}

	resolved, err := security.Resolve(cfg.Profile, cfg.Mode, cfg.AllowedDomains, toolNames, cfg.Capabilities)
	if err != nil {
		return fmt.Errorf("resolving egress: %w", err)
	}

	bc.EgressResolved = resolved

	// Set egress fields on spec
	if bc.Spec != nil {
		bc.Spec.EgressProfile = string(resolved.Profile)
		bc.Spec.EgressMode = string(resolved.Mode)
	}

	// Write egress_allowlist.json
	data, err := security.GenerateAllowlistJSON(resolved)
	if err != nil {
		return fmt.Errorf("generating egress allowlist: %w", err)
	}

	compiledDir := filepath.Join(bc.Opts.OutputDir, "compiled")
	if err := os.MkdirAll(compiledDir, 0755); err != nil {
		return fmt.Errorf("creating compiled directory: %w", err)
	}

	outPath := filepath.Join(compiledDir, "egress_allowlist.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing egress_allowlist.json: %w", err)
	}

	bc.AddFile("compiled/egress_allowlist.json", outPath)
	return nil
}
