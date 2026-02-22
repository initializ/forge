package skills

// SkillEntry represents a single tool/skill parsed from a skills.md file.
type SkillEntry struct {
	Name        string
	Description string
	InputSpec   string
	OutputSpec  string
	Metadata    *SkillMetadata     // nil if no frontmatter
	ForgeReqs   *SkillRequirements // convenience: extracted from metadata.forge.requires
}

// SkillMetadata holds the full frontmatter parsed from YAML between --- delimiters.
// Uses map to tolerate unknown namespaces (e.g. clawdbot:).
type SkillMetadata struct {
	Name        string                    `yaml:"name,omitempty"`
	Description string                    `yaml:"description,omitempty"`
	Metadata    map[string]map[string]any `yaml:"metadata,omitempty"`
}

// ForgeSkillMeta holds Forge-specific metadata from the "forge" namespace.
type ForgeSkillMeta struct {
	Requires *SkillRequirements `yaml:"requires,omitempty" json:"requires,omitempty"`
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
