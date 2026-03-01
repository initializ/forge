package contract

// SkillDescriptor describes a skill available in a registry.
type SkillDescriptor struct {
	Name          string
	DisplayName   string
	Description   string
	Category      string
	Tags          []string
	RequiredEnv   []string
	OneOfEnv      []string
	OptionalEnv   []string
	RequiredBins  []string
	EgressDomains []string
	DeniedTools   []string
	TimeoutHint   int         // suggested timeout in seconds (0 = use default)
	Provenance    *Provenance `json:"provenance,omitempty"`
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
	Metadata    map[string]map[string]any `yaml:"metadata,omitempty"`
}

// ForgeSkillMeta holds Forge-specific metadata from the "forge" namespace.
type ForgeSkillMeta struct {
	Requires      *SkillRequirements `yaml:"requires,omitempty" json:"requires,omitempty"`
	EgressDomains []string           `yaml:"egress_domains,omitempty" json:"egress_domains,omitempty"`
	DeniedTools   []string           `yaml:"denied_tools,omitempty" json:"denied_tools,omitempty"`
}

// SkillRequirements declares CLI binaries and environment variables a skill needs.
type SkillRequirements struct {
	Bins []string         `yaml:"bins,omitempty" json:"bins,omitempty"`
	Env  *EnvRequirements `yaml:"env,omitempty" json:"env,omitempty"`
}

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
	Bins           []string   // union of all bins, deduplicated, sorted
	EnvRequired    []string   // union of required vars (promoted from optional if needed)
	EnvOneOf       [][]string // separate groups per skill (not merged across skills)
	EnvOptional    []string   // union of optional vars minus those promoted to required
	MaxTimeoutHint int        // maximum timeout_hint across all skills (seconds)
	DeniedTools    []string   // union of denied tools across skills, deduplicated, sorted
	EgressDomains  []string   // union of egress domains across skills, deduplicated, sorted
}

// DerivedCLIConfig holds auto-derived cli_execute configuration from skill requirements.
type DerivedCLIConfig struct {
	AllowedBinaries []string
	EnvPassthrough  []string
	TimeoutHint     int      // suggested timeout in seconds (0 = use default)
	DeniedTools     []string // tools to remove from registry before LLM execution
	EgressDomains   []string // additional egress domains from skills
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
