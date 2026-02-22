package build

import (
	"context"

	coreskills "github.com/initializ/forge/forge-core/skills"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

// RequirementsStage validates skill requirements and populates the agent spec.
type RequirementsStage struct{}

func (s *RequirementsStage) Name() string { return "validate-requirements" }

func (s *RequirementsStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if bc.SkillRequirements == nil {
		return nil
	}

	reqs, ok := bc.SkillRequirements.(*coreskills.AggregatedRequirements)
	if !ok {
		return nil
	}

	// Check binaries — warnings only (may be installed in container)
	binDiags := coreskills.BinDiagnostics(reqs.Bins)
	for _, d := range binDiags {
		bc.AddWarning(d.Message)
	}

	// Populate agent spec requirements
	if bc.Spec != nil {
		bc.Spec.Requirements = &agentspec.AgentRequirements{
			Bins:        reqs.Bins,
			EnvRequired: reqs.EnvRequired,
			EnvOptional: reqs.EnvOptional,
		}

		// Auto-derive cli_execute config
		derived := coreskills.DeriveCLIConfig(reqs)
		if derived != nil && len(derived.AllowedBinaries) > 0 {
			// Find existing cli_execute tool in spec and merge
			found := false
			for i, tool := range bc.Spec.Tools {
				if tool.Name == "cli_execute" {
					found = true
					// Merge with existing ForgeMeta
					if tool.ForgeMeta == nil {
						tool.ForgeMeta = &agentspec.ForgeToolMeta{}
					}
					if len(tool.ForgeMeta.AllowedBinaries) == 0 {
						tool.ForgeMeta.AllowedBinaries = derived.AllowedBinaries
					}
					if len(tool.ForgeMeta.EnvPassthrough) == 0 {
						tool.ForgeMeta.EnvPassthrough = derived.EnvPassthrough
					}
					bc.Spec.Tools[i] = tool
					break
				}
			}

			// If no cli_execute tool exists, add one with derived config
			if !found {
				bc.Spec.Tools = append(bc.Spec.Tools, agentspec.ToolSpec{
					Name:     "cli_execute",
					Category: "builtin",
					ForgeMeta: &agentspec.ForgeToolMeta{
						AllowedBinaries: derived.AllowedBinaries,
						EnvPassthrough:  derived.EnvPassthrough,
					},
				})
			}
		}
	}

	return nil
}
