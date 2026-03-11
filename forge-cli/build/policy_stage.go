package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
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
				{Type: "no_pii"},
				{Type: "jailbreak_protection"},
				{Type: "no_secrets"},
			},
		}
	}

	// Inject aggregated skill guardrails if present
	if bc.SkillRequirements != nil {
		if reqs, ok := bc.SkillRequirements.(*contract.AggregatedRequirements); ok && reqs.SkillGuardrails != nil {
			sg := reqs.SkillGuardrails
			rules := &agentspec.SkillGuardrailRules{}
			for _, c := range sg.DenyCommands {
				rules.DenyCommands = append(rules.DenyCommands, agentspec.CommandFilter{
					Pattern: c.Pattern,
					Message: c.Message,
				})
			}
			for _, o := range sg.DenyOutput {
				rules.DenyOutput = append(rules.DenyOutput, agentspec.OutputFilter{
					Pattern: o.Pattern,
					Action:  o.Action,
				})
			}
			for _, p := range sg.DenyPrompts {
				rules.DenyPrompts = append(rules.DenyPrompts, agentspec.CommandFilter{
					Pattern: p.Pattern,
					Message: p.Message,
				})
			}
			for _, r := range sg.DenyResponses {
				rules.DenyResponses = append(rules.DenyResponses, agentspec.CommandFilter{
					Pattern: r.Pattern,
					Message: r.Message,
				})
			}
			if len(rules.DenyCommands) > 0 || len(rules.DenyOutput) > 0 || len(rules.DenyPrompts) > 0 || len(rules.DenyResponses) > 0 {
				bc.Spec.PolicyScaffold.SkillGuardrails = rules
			}
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
