package build

import (
	"context"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/packaging"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/requirements"
	"github.com/initializ/forge/forge-skills/resolver"
)

// RequirementsStage validates skill requirements and populates the agent spec.
type RequirementsStage struct{}

// ensureBin appends a BinRequirement for name unless it's already present.
func ensureBin(reqs []contract.BinRequirement, name string) []contract.BinRequirement {
	for _, b := range reqs {
		if b.Name == name {
			return reqs
		}
	}
	return append(reqs, contract.BinRequirement{Name: name})
}

func (s *RequirementsStage) Name() string { return "validate-requirements" }

func (s *RequirementsStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	reqs, _ := bc.SkillRequirements.(*contract.AggregatedRequirements)

	// Nothing to do unless a skill declared requirements OR shipped a
	// requirements.txt (which alone forces python3/pip into the image, #405 D1).
	if reqs == nil && len(bc.SkillPipRequirements) == 0 {
		return nil
	}

	var binReqs []contract.BinRequirement
	browserCapability := false

	if reqs != nil {
		// Check binaries — warnings only (may be installed in container)
		binDiags := resolver.BinDiagnostics(reqs.Bins)
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
			derived := requirements.DeriveCLIConfig(reqs)
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

		binReqs = reqs.BinRequirements
		for _, c := range reqs.Capabilities {
			if c == contract.CapabilityBrowser {
				browserCapability = true
				break
			}
		}
	}

	// The browser capability needs a Chromium binary in the image, but it is
	// not a skill-declared bin. Inject a synthetic requirement so the smart
	// Dockerfile installs it (resolved via the well-known image registry) —
	// and only for browser agents, keeping the browser optional.
	if browserCapability {
		hasChromium := false
		for _, b := range binReqs {
			if b.Name == "chromium" {
				hasChromium = true
				break
			}
		}
		if !hasChromium {
			binReqs = append(binReqs, contract.BinRequirement{Name: "chromium"})
		}
	}

	// A skill shipping a requirements.txt needs the Python interpreter and pip
	// in the image to install its deps (#405 D1). Inject them synthetically —
	// like the browser capability's chromium — so the operator doesn't have to
	// list python3/pip in requires.bins by hand. No-op when they're already
	// declared.
	pythonInjected := false
	if len(bc.SkillPipRequirements) > 0 {
		before := len(binReqs)
		binReqs = ensureBin(binReqs, "python3")
		binReqs = ensureBin(binReqs, "pip")
		pythonInjected = len(binReqs) > before
	}

	// Build BinManifest from rich requirements for smart Dockerfile generation
	if len(binReqs) > 0 {
		manifest := &packaging.BinManifest{
			Requirements: binReqs,
			SkillOrigin:  make(map[string]string),
		}
		if browserCapability {
			manifest.SkillOrigin["chromium"] = "capability:browser"
		}
		if pythonInjected {
			manifest.SkillOrigin["python3"] = "skill:requirements.txt"
			manifest.SkillOrigin["pip"] = "skill:requirements.txt"
		}
		// Populate skill origins from entries if available
		if bc.SkillEntries != nil {
			if entries, ok := bc.SkillEntries.([]contract.SkillEntry); ok {
				for _, e := range entries {
					if e.ForgeReqs != nil {
						for _, b := range e.ForgeReqs.Bins {
							if _, exists := manifest.SkillOrigin[b.Name]; !exists {
								manifest.SkillOrigin[b.Name] = e.Name
							}
						}
					}
				}
			}
		}
		bc.BinManifest = manifest
	}

	return nil
}
