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

	// Framework
	ForgeFramework bool
	ForgeVersion   string // forge CLI version for GitHub release download (e.g. "v0.9.0")

	// Multi-stage build
	HasBinStage bool // true when a bins stage exists with installed binaries

	// Per-binary plumbing emitted in the application stage. See
	// forge-core/packaging.DockerfileFragments for the full design
	// (issue #149).
	BinCopies          []string // "COPY --from=<stage> /path /path" lines, one per binary.
	RuntimeAptPackages []string // runtime apt packages installed in the application stage (debian).
	RuntimeApkPackages []string // runtime apk packages installed in the application stage (alpine).
	PathExtensions     []string // PATH directories for non-standard binary locations.

	// KeepRuntimeCurl is true when curl is a declared runtime binary (present
	// in RuntimeAptPackages/RuntimeApkPackages). The forge-framework bootstrap
	// borrows curl to fetch the forge binary and then purges it; when a skill
	// legitimately requires curl at runtime, that purge would clobber it, so
	// the Dockerfile template skips the purge in that case.
	KeepRuntimeCurl bool
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

	// Set forge framework flag and version.
	// Skip the remote download when a local binary override for "forge" is provided
	// (--local-bin forge=... or forge.yaml bin_overrides.forge.local), since the
	// local binary is already installed via the bins stage.
	if bc.Config != nil && bc.Config.Framework == "forge" {
		hasLocalForge := false
		if override, ok := bc.Config.Package.BinOverrides["forge"]; ok && override.LocalPath != "" {
			hasLocalForge = true
		}
		if !hasLocalForge {
			d.ForgeFramework = true
			v := bc.ForgeCLIVersion
			if v == "" || v == "dev" {
				v = "latest"
			} else if v[0] != 'v' {
				v = "v" + v
			}
			d.ForgeVersion = v
		}
	}

	return d
}
