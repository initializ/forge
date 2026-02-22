package compiler

import (
	"encoding/json"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

// TemplateSpecData holds data used by Dockerfile and K8s templates.
type TemplateSpecData struct {
	AgentID       string
	Version       string
	Runtime       *TemplateRuntimeData
	Registry      string
	NetworkPolicy *NetworkPolicyData

	// Container packaging extensions
	EgressProfile        string
	EgressMode           string
	ToolInterfaceVersion string
	SkillsCount          int
	HasSkills            bool
	DevBuild             bool
	ProdBuild            bool

	// Skill requirements
	RequiredEnvVars []string
	OptionalEnvVars []string
	RequiredBins    []string
}

// TemplateRuntimeData holds runtime-specific template data.
type TemplateRuntimeData struct {
	Image          string
	Port           int
	Entrypoint     string // Pre-formatted JSON array string, e.g. ["python", "agent.py"]
	Env            map[string]string
	DepsFile       string
	DepsInstallCmd string
	HealthCheck    string
	User           string
	ModelEnv       map[string]string
}

// NetworkPolicyData holds network policy template data.
type NetworkPolicyData struct {
	DenyAll bool
}

// BuildTemplateDataFromSpec creates template data from an AgentSpec.
func BuildTemplateDataFromSpec(spec *agentspec.AgentSpec) *TemplateSpecData {
	d := &TemplateSpecData{
		AgentID:       spec.AgentID,
		Version:       spec.Version,
		NetworkPolicy: &NetworkPolicyData{DenyAll: true}, // default: deny all egress
	}
	if spec.Runtime != nil {
		ep, _ := json.Marshal(spec.Runtime.Entrypoint)
		env := spec.Runtime.Env

		// Build ModelEnv from model config
		var modelEnv map[string]string
		if spec.Model != nil && spec.Model.Provider != "" {
			modelEnv = map[string]string{
				"FORGE_MODEL_PROVIDER": spec.Model.Provider,
			}
			if spec.Model.Name != "" {
				modelEnv["FORGE_MODEL_NAME"] = spec.Model.Name
			}
		}

		// Merge ModelEnv into Env for deployment template (ModelEnv takes precedence)
		if len(modelEnv) > 0 {
			if env == nil {
				env = make(map[string]string)
			}
			for k, v := range modelEnv {
				env[k] = v
			}
		}

		d.Runtime = &TemplateRuntimeData{
			Image:          spec.Runtime.Image,
			Port:           spec.Runtime.Port,
			Entrypoint:     string(ep),
			Env:            env,
			DepsFile:       spec.Runtime.DepsFile,
			DepsInstallCmd: spec.Runtime.DepsInstallCmd,
			HealthCheck:    spec.Runtime.HealthCheck,
			User:           spec.Runtime.User,
			ModelEnv:       modelEnv,
		}
	}
	return d
}

// BuildTemplateDataFromContext creates template data from an AgentSpec and BuildContext.
func BuildTemplateDataFromContext(spec *agentspec.AgentSpec, bc *pipeline.BuildContext) *TemplateSpecData {
	d := BuildTemplateDataFromSpec(spec)
	d.DevBuild = bc.DevMode
	d.ProdBuild = bc.ProdMode
	d.SkillsCount = bc.SkillsCount
	d.HasSkills = bc.SkillsCount > 0
	d.EgressProfile = spec.EgressProfile
	d.EgressMode = spec.EgressMode
	d.ToolInterfaceVersion = spec.ToolInterfaceVersion

	// Populate skill requirements from build context
	if spec.Requirements != nil {
		d.RequiredEnvVars = spec.Requirements.EnvRequired
		d.OptionalEnvVars = spec.Requirements.EnvOptional
		d.RequiredBins = spec.Requirements.Bins
	}

	return d
}
