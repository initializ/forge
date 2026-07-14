package contract

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// SkillDescriptor describes a skill available in a registry.
type SkillDescriptor struct {
	Name               string
	DisplayName        string
	Description        string
	Category           string
	Tags               []string
	Icon               string
	RequiredEnv        []string
	OneOfEnv           []string
	OptionalEnv        []string
	RequiredBins       []string
	EgressDomains      []string
	DeniedTools        []string
	Capabilities       []string    // runtime capabilities from requires.capabilities (e.g. "browser")
	TrustHints         *TrustHints // author-declared behavior hints (analyzer consistency checks)
	HasDenyOutput      bool        // whether guardrails.deny_output declares at least one pattern
	AllowSensitiveFill bool        // whether guardrails.browser.allow_sensitive_fill opts into password/payment fill
	TimeoutHint        int         // suggested timeout in seconds (0 = use default)
	Provenance         *Provenance `json:"provenance,omitempty"`
}

// SkillEntry represents a single tool/skill parsed from a SKILL.md file.
type SkillEntry struct {
	Name         string
	Description  string
	InputSpec    string
	OutputSpec   string
	OutputFormat string
	Body         string             // full markdown body after frontmatter
	Metadata     *SkillMetadata     // nil if no frontmatter
	ForgeReqs    *SkillRequirements // convenience: extracted from metadata.forge.requires
}

// SkillMetadata holds the full frontmatter parsed from YAML between --- delimiters.
// Uses map to tolerate unknown namespaces (e.g. clawdbot:).
type SkillMetadata struct {
	Name        string                    `yaml:"name,omitempty"`
	Description string                    `yaml:"description,omitempty"`
	Category    string                    `yaml:"category,omitempty"`
	Tags        []string                  `yaml:"tags,omitempty"`
	Icon        string                    `yaml:"icon,omitempty"`
	Metadata    map[string]map[string]any `yaml:"metadata,omitempty"`
}

// ForgeSkillMeta holds Forge-specific metadata from the "forge" namespace.
type ForgeSkillMeta struct {
	// Runtime selects how the skill's tool is executed. Valid values:
	//
	//   - "" or "script" (default) — the skill body is materialized as a
	//     bash script and invoked via `bash <scriptPath> <jsonArgs>`. The
	//     skill's `## Tool: <name>` entries each look for a matching
	//     `skills/<dir>/scripts/<name>.sh` or `skills/scripts/<name>.sh`.
	//   - "binary" — the skill IS an external binary. The first entry of
	//     `metadata.forge.requires.bins` is the executable name; the runner
	//     resolves it via `exec.LookPath` and calls it with `<jsonArgs>` as
	//     the positional argument (same input convention as scripts, so the
	//     binary's stdin/stdout contract is identical). The skill body is
	//     documentation only — no script file is materialized.
	//
	// Binary skills let an OTel-instrumented child process inherit the
	// parent agent's `tool.<name>` span via TRACEPARENT env propagation
	// (issue #182). Wrapping the binary in a bash skill works too, but
	// adds a fork and a wrapper to maintain.
	Runtime  string             `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Requires *SkillRequirements `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Uses declares the skill's governed tool dependencies — references into
	// the PLATFORM tool registry (the org-scoped catalog of admitted tools),
	// chosen as binary or MCP with a selected operation subset. Additive and
	// DECLARATIVE from forge's perspective: the platform resolves and
	// validates refs at authoring/build time and materializes the runtime
	// wiring (mcp servers, allowlists); forge parses the block so the
	// declaration travels with the skill, and does not gate on it.
	Uses          []SkillToolDependency `yaml:"uses,omitempty" json:"uses,omitempty"`
	EgressDomains []string              `yaml:"egress_domains,omitempty" json:"egress_domains,omitempty"`
	DeniedTools   []string              `yaml:"denied_tools,omitempty" json:"denied_tools,omitempty"`
	WorkflowPhase string                `yaml:"workflow_phase,omitempty" json:"workflow_phase,omitempty"`
	Guardrails    *SkillGuardrailConfig `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
	TrustHints    *TrustHints           `yaml:"trust_hints,omitempty" json:"trust_hints,omitempty"`
}

// TrustHints are the skill author's self-declared behavior hints, checked for
// consistency by the security analyzer (they never raise trust, only flag
// contradictions — e.g. declaring the browser capability while claiming
// network: false).
//
// Network and Shell are pointer-bools because absence and explicit false are
// different statements: only an explicit false contradicts a network-requiring
// capability.
type TrustHints struct {
	Network *bool `yaml:"network,omitempty" json:"network,omitempty"`
	// Filesystem is a mode string as used by existing skills: "read",
	// "write", "none", or empty (undeclared).
	Filesystem string `yaml:"filesystem,omitempty" json:"filesystem,omitempty"`
	Shell      *bool  `yaml:"shell,omitempty" json:"shell,omitempty"`
}

// Valid Runtime values for ForgeSkillMeta.Runtime.
const (
	SkillRuntimeScript = "script" // bash-script execution (default)
	SkillRuntimeBinary = "binary" // external binary on PATH
)

// SkillGuardrailConfig declares domain-specific guardrails for a skill.
type SkillGuardrailConfig struct {
	DenyCommands  []SkillCommandFilter    `yaml:"deny_commands,omitempty" json:"deny_commands,omitempty"`
	DenyOutput    []SkillOutputFilter     `yaml:"deny_output,omitempty" json:"deny_output,omitempty"`
	DenyPrompts   []SkillCommandFilter    `yaml:"deny_prompts,omitempty" json:"deny_prompts,omitempty"`
	DenyResponses []SkillCommandFilter    `yaml:"deny_responses,omitempty" json:"deny_responses,omitempty"`
	Browser       *SkillBrowserGuardrails `yaml:"browser,omitempty" json:"browser,omitempty"`
}

// SkillBrowserGuardrails tunes browser tool safety for skills declaring the
// browser capability.
type SkillBrowserGuardrails struct {
	// AllowSensitiveFill opts in to browser_fill on password and payment
	// fields (input type=password, autocomplete cc-*/…-password), which are
	// refused by default.
	AllowSensitiveFill bool `yaml:"allow_sensitive_fill,omitempty" json:"allow_sensitive_fill,omitempty"`
}

// SkillCommandFilter blocks tool execution when the command matches.
type SkillCommandFilter struct {
	Pattern string `yaml:"pattern" json:"pattern"` // regex matched against "binary arg1 arg2 ..."
	Message string `yaml:"message" json:"message"` // error returned to LLM
}

// SkillOutputFilter blocks or redacts tool output matching a pattern.
type SkillOutputFilter struct {
	Pattern string `yaml:"pattern" json:"pattern"` // regex matched against tool output
	Action  string `yaml:"action" json:"action"`   // "block" or "redact"
}

// BinRequirement describes a binary dependency with optional install metadata.
// It supports both scalar YAML ("jq") and mapping YAML ({name: jq, version: "1.6"}).
type BinRequirement struct {
	Name        string   `yaml:"name" json:"name"`
	Version     string   `yaml:"version,omitempty" json:"version,omitempty"`
	Optional    bool     `yaml:"optional,omitempty" json:"optional,omitempty"`
	AptPackage  string   `yaml:"apt,omitempty" json:"apt,omitempty"`
	ApkPackage  string   `yaml:"apk,omitempty" json:"apk,omitempty"`
	DirectURL   string   `yaml:"url,omitempty" json:"url,omitempty"`
	Dest        string   `yaml:"dest,omitempty" json:"dest,omitempty"`
	Chmod       string   `yaml:"chmod,omitempty" json:"chmod,omitempty"`
	CustomLines []string `yaml:"run,omitempty" json:"run,omitempty"`
}

// UnmarshalYAML handles both scalar ("jq") and mapping ({name: jq, ...}) YAML nodes.
func (b *BinRequirement) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		b.Name = value.Value
		if b.Name == "" {
			return fmt.Errorf("bin requirement: name cannot be empty")
		}
		return nil
	case yaml.MappingNode:
		// Decode into an alias to avoid infinite recursion.
		type binReqAlias BinRequirement
		var alias binReqAlias
		if err := value.Decode(&alias); err != nil {
			return fmt.Errorf("bin requirement: %w", err)
		}
		if alias.Name == "" {
			return fmt.Errorf("bin requirement: name is required in mapping form")
		}
		*b = BinRequirement(alias)
		return nil
	default:
		return fmt.Errorf("bin requirement: expected string or mapping, got %v", value.Kind)
	}
}

// SkillToolDependency is one governed tool dependency: a reference to a
// platform tool-registry entry (by its stable key) with the operations this
// skill selects. Type distinguishes binary vs MCP registry entries.
type SkillToolDependency struct {
	Type       string   `yaml:"type" json:"type"` // binary | mcp
	Ref        string   `yaml:"ref" json:"ref"`   // registry entry key, e.g. "mcp.linear"
	Operations []string `yaml:"operations,omitempty" json:"operations,omitempty"`
}

// SkillRequirements declares CLI binaries, environment variables, and runtime
// capabilities a skill needs.
type SkillRequirements struct {
	Bins []BinRequirement `yaml:"bins,omitempty" json:"bins,omitempty"`
	Env  *EnvRequirements `yaml:"env,omitempty" json:"env,omitempty"`
	// Capabilities are opt-in runtime capabilities the skill needs the runner
	// to provide (conditional tool families, not binaries). Currently
	// recognized: "browser".
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// CapabilityBrowser gates registration of the browser_* tool family: a
// proxied headless Chromium the LLM drives via indexed page digests.
const CapabilityBrowser = "browser"

// knownCapabilities is the set of runtime capabilities the runner can provide.
// A skill declaring anything outside this set is rejected at parse time — an
// unknown capability (usually a typo) would otherwise silently register no
// tools and install no dependency, with zero diagnostics.
var knownCapabilities = map[string]bool{
	CapabilityBrowser: true,
}

// IsKnownCapability reports whether name is a capability the runner recognizes.
func IsKnownCapability(name string) bool { return knownCapabilities[name] }

// EnvRequirements declares environment variable requirements at different levels.
type EnvRequirements struct {
	Required []string `yaml:"required,omitempty" json:"required,omitempty"`
	OneOf    []string `yaml:"one_of,omitempty" json:"one_of,omitempty"`
	Optional []string `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// CompiledSkills holds the result of compiling skill entries.
type CompiledSkills struct {
	Skills  []CompiledSkill `json:"skills"`
	Count   int             `json:"count"`
	Version string          `json:"version"`
	Prompt  string          `json:"-"` // written separately as prompt.txt
}

// CompiledSkill represents a single compiled skill.
type CompiledSkill struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Category     string   `json:"category,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	InputSpec    string   `json:"input_spec,omitempty"`
	OutputSpec   string   `json:"output_spec,omitempty"`
	OutputFormat string   `json:"output_format,omitempty"`
	Body         string   `json:"body,omitempty"`
}

// SkillFilter defines criteria for filtering skill lists.
type SkillFilter struct {
	Category string
	Tags     []string
}

// AggregatedRequirements is the union of all skill requirements.
type AggregatedRequirements struct {
	Bins            []string              // union of all bin names, deduplicated, sorted
	BinRequirements []BinRequirement      // rich requirements, deduplicated by name (richer entry wins)
	EnvRequired     []string              // union of required vars (promoted from optional if needed)
	EnvOneOf        [][]string            // separate groups per skill (not merged across skills)
	EnvOptional     []string              // union of optional vars minus those promoted to required
	MaxTimeoutHint  int                   // maximum timeout_hint across all skills (seconds)
	DeniedTools     []string              // union of denied tools across skills, deduplicated, sorted
	EgressDomains   []string              // union of egress domains across skills, deduplicated, sorted
	WorkflowPhases  []string              // union of workflow_phase values across skills, deduplicated, sorted
	SkillGuardrails *SkillGuardrailConfig // aggregated guardrails from all skills
	Capabilities    []string              // union of requires.capabilities across skills, deduplicated, sorted
}

// DerivedCLIConfig holds auto-derived cli_execute configuration from skill requirements.
type DerivedCLIConfig struct {
	AllowedBinaries []string
	EnvPassthrough  []string
	TimeoutHint     int      // suggested timeout in seconds (0 = use default)
	DeniedTools     []string // tools to remove from registry before LLM execution
	EgressDomains   []string // additional egress domains from skills
	WorkflowPhases  []string // workflow phases from skills (edit, finalize, query)
}

// DerivedBrowserConfig signals that at least one active skill declared the
// browser capability. A non-nil value means the runner should register the
// browser_* tool family (subject to a Chromium binary and egress proxy being
// available). Runtime concerns — binary path, headless mode, proxy URL — are
// deliberately not here; they belong to the runner and the browser package.
type DerivedBrowserConfig struct {
	// SourceSkills names the skills that declared the capability, for logs
	// and actionable startup errors.
	SourceSkills []string
	// AllowSensitiveFill permits browser_fill on password/payment fields.
	// Set via skill guardrail opt-in; default false.
	AllowSensitiveFill bool
}

// TrustLevel indicates the trust classification of a skill.
type TrustLevel string

const (
	TrustBuiltin   TrustLevel = "builtin"   // embedded in binary
	TrustVerified  TrustLevel = "verified"  // signature checked
	TrustLocal     TrustLevel = "local"     // user's project, no signature
	TrustUntrusted TrustLevel = "untrusted" // unknown origin
)

// Provenance records the origin and integrity metadata for a skill.
type Provenance struct {
	Source   string     `json:"source"`              // "embedded", "local", "remote"
	Author   string     `json:"author,omitempty"`    // signer identity
	Version  string     `json:"version,omitempty"`   // skill version from frontmatter
	Trust    TrustLevel `json:"trust"`               // trust classification
	Checksum string     `json:"checksum"`            // "sha256:<hex>" of SKILL.md content
	SignedBy string     `json:"signed_by,omitempty"` // key ID if signed, empty if not
}

// EnvSource describes where an environment variable was found.
type EnvSource string

const (
	EnvSourceOS      EnvSource = "environment"
	EnvSourceDotEnv  EnvSource = "dotenv"
	EnvSourceConfig  EnvSource = "config"
	EnvSourceMissing EnvSource = "missing"
)

// ValidationDiagnostic represents a single validation finding.
type ValidationDiagnostic struct {
	Level   string // "error", "warning", "info"
	Message string
	Var     string
}
