package pipeline

import (
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/types"
)

// BuildContext carries all state through the build pipeline.
type BuildContext struct {
	Opts           PipelineOptions
	Config         *types.ForgeConfig
	Spec           *agentspec.AgentSpec
	GeneratedFiles map[string]string // relPath -> absPath
	Warnings       []string
	Verbose        bool
	PluginConfig   *plugins.AgentConfig // Set by FrameworkAdapterStage
	WrapperFile    string               // Relative path to generated wrapper (empty = no wrapper)

	// Container packaging extensions
	DevMode            bool
	ProdMode           bool
	EgressResolved     any // *egress.EgressConfig (avoid import cycle)
	SkillRequirements  any // *skills.AggregatedRequirements (avoid import cycle)
	SkillEntries       any // []contract.SkillEntry (avoid import cycle)
	SecurityAudit      any // *analyzer.AuditReport (avoid import cycle)
	SkillsCount        int
	ToolCategoryCounts map[string]int

	// Bin resolution for smart Dockerfile generation
	BinManifest  any // *packaging.BinManifest (avoid import cycle)
	PreferAlpine bool
	PreferSlim   bool

	// ForgeCLIVersion is the version of the forge CLI binary (e.g. "v0.9.0").
	// Used to pull the correct release when framework is "forge".
	ForgeCLIVersion string

	// LocalBins maps binary name → host file path (from --local-bin flags).
	LocalBins map[string]string
}

// NewBuildContext creates a BuildContext with the given options and initialized maps.
func NewBuildContext(opts PipelineOptions) *BuildContext {
	return &BuildContext{
		Opts:           opts,
		GeneratedFiles: make(map[string]string),
	}
}

// AddFile records a generated file in the build context.
func (bc *BuildContext) AddFile(relPath, absPath string) {
	bc.GeneratedFiles[relPath] = absPath
}

// AddWarning appends a warning message to the build context.
func (bc *BuildContext) AddWarning(msg string) {
	bc.Warnings = append(bc.Warnings, msg)
}
