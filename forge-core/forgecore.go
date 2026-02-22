// Package forgecore provides a high-level API surface for embedding
// Forge's compiler, validator, and runtime engine as a library.
//
// This is the primary entry point for external consumers (e.g. Command)
// who want to use Forge's capabilities without importing CLI dependencies.
package forgecore

import (
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/compiler"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/skills"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/validate"
)

// ─── Compile API ──────────────────────────────────────────────────────

// CompileRequest contains the inputs for compiling a ForgeConfig into an AgentSpec.
type CompileRequest struct {
	Config       *types.ForgeConfig
	PluginConfig *plugins.AgentConfig  // optional framework plugin config
	SkillEntries []skills.SkillEntry   // optional skill entries
}

// CompileResult contains the outputs of a successful compilation.
type CompileResult struct {
	Spec           *agentspec.AgentSpec
	CompiledSkills *skills.CompiledSkills // nil if no skills
	EgressConfig   *security.EgressConfig
	Allowlist      []byte // JSON-encoded allowlist
}

// Compile transforms a ForgeConfig into a fully-resolved AgentSpec with
// security configuration, skill compilation, and optional plugin merging.
func Compile(req CompileRequest) (*CompileResult, error) {
	spec := compiler.ConfigToAgentSpec(req.Config)

	// Merge plugin configuration if provided
	if req.PluginConfig != nil {
		compiler.MergePluginConfig(spec, req.PluginConfig)
	}

	// Compile skills if provided
	var cs *skills.CompiledSkills
	if len(req.SkillEntries) > 0 {
		var err error
		cs, err = skills.Compile(req.SkillEntries)
		if err != nil {
			return nil, err
		}
	}

	// Resolve egress configuration
	var toolNames []string
	for _, t := range spec.Tools {
		toolNames = append(toolNames, t.Name)
	}

	egressCfg, err := security.Resolve(
		req.Config.Egress.Profile,
		req.Config.Egress.Mode,
		req.Config.Egress.AllowedDomains,
		toolNames,
		nil, // capabilities resolved from profile
	)
	if err != nil {
		return nil, err
	}

	allowlist, err2 := security.GenerateAllowlistJSON(egressCfg)
	if err2 != nil {
		return nil, err2
	}

	// Map egress to spec fields
	spec.EgressProfile = string(egressCfg.Profile)
	spec.EgressMode = string(egressCfg.Mode)

	return &CompileResult{
		Spec:           spec,
		CompiledSkills: cs,
		EgressConfig:   egressCfg,
		Allowlist:      allowlist,
	}, nil
}

// ─── Validate API ─────────────────────────────────────────────────────

// ValidateConfig checks a ForgeConfig for errors and warnings.
func ValidateConfig(cfg *types.ForgeConfig) *validate.ValidationResult {
	return validate.ValidateForgeConfig(cfg)
}

// ValidateAgentSpec validates raw JSON bytes against the AgentSpec v1.0 schema.
func ValidateAgentSpec(jsonData []byte) ([]string, error) {
	return validate.ValidateAgentSpec(jsonData)
}

// ValidateCommandCompat checks an AgentSpec against Command platform requirements.
func ValidateCommandCompat(spec *agentspec.AgentSpec) *validate.ValidationResult {
	return validate.ValidateCommandCompat(spec)
}

// SimulateImport simulates what Command's import API would produce from an AgentSpec.
func SimulateImport(spec *agentspec.AgentSpec) *validate.ImportSimResult {
	return validate.SimulateImport(spec)
}

// ─── Runtime API ──────────────────────────────────────────────────────

// RuntimeConfig configures the LLM agent runtime.
type RuntimeConfig struct {
	LLMClient    llm.Client
	Tools        runtime.ToolExecutor
	Hooks        *runtime.HookRegistry
	SystemPrompt string
	MaxIterations int
	Guardrails   *runtime.GuardrailEngine // optional
	Logger       runtime.Logger           // optional
}

// NewRuntime creates a new LLMExecutor configured for agent execution.
func NewRuntime(cfg RuntimeConfig) *runtime.LLMExecutor {
	return runtime.NewLLMExecutor(runtime.LLMExecutorConfig{
		Client:        cfg.LLMClient,
		Tools:         cfg.Tools,
		Hooks:         cfg.Hooks,
		SystemPrompt:  cfg.SystemPrompt,
		MaxIterations: cfg.MaxIterations,
	})
}
