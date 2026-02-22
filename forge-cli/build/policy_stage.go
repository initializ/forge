package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

// PolicyStage generates the policy scaffold file.
type PolicyStage struct{}

func (s *PolicyStage) Name() string { return "generate-policy-scaffold" }

func (s *PolicyStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if bc.Spec.PolicyScaffold == nil {
		bc.Spec.PolicyScaffold = &agentspec.PolicyScaffold{
			Guardrails: []agentspec.Guardrail{
				{
					Type:   "content_filter",
					Config: map[string]any{"enabled": true},
				},
			},
		}
	}

	data, err := json.MarshalIndent(bc.Spec.PolicyScaffold, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling policy scaffold: %w", err)
	}

	outPath := filepath.Join(bc.Opts.OutputDir, "policy-scaffold.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing policy-scaffold.json: %w", err)
	}

	bc.AddFile("policy-scaffold.json", outPath)
	return nil
}
