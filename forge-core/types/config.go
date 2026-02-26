// Package types holds configuration types for forge.yaml.
package types

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ForgeConfig represents the top-level forge.yaml configuration.
type ForgeConfig struct {
	AgentID    string       `yaml:"agent_id"`
	Version    string       `yaml:"version"`
	Framework  string       `yaml:"framework"`
	Entrypoint string       `yaml:"entrypoint"`
	Model      ModelRef     `yaml:"model,omitempty"`
	Tools      []ToolRef    `yaml:"tools,omitempty"`
	Channels   []string     `yaml:"channels,omitempty"`
	Registry   string       `yaml:"registry,omitempty"`
	Egress     EgressRef    `yaml:"egress,omitempty"`
	Skills     SkillsRef    `yaml:"skills,omitempty"`
	Memory     MemoryConfig `yaml:"memory,omitempty"`
}

// MemoryConfig configures agent memory persistence and compaction.
type MemoryConfig struct {
	Persistence  *bool   `yaml:"persistence,omitempty"` // default: true
	SessionsDir  string  `yaml:"sessions_dir,omitempty"`
	TriggerRatio float64 `yaml:"trigger_ratio,omitempty"`
	CharBudget   int     `yaml:"char_budget,omitempty"`

	// Long-term memory (persistent cross-session knowledge).
	LongTerm          *bool   `yaml:"long_term,omitempty"`            // default: false
	MemoryDir         string  `yaml:"memory_dir,omitempty"`           // default: .forge/memory
	EmbeddingProvider string  `yaml:"embedding_provider,omitempty"`   // auto-detect from LLM
	EmbeddingModel    string  `yaml:"embedding_model,omitempty"`      // provider default
	VectorWeight      float64 `yaml:"vector_weight,omitempty"`        // default: 0.7
	KeywordWeight     float64 `yaml:"keyword_weight,omitempty"`       // default: 0.3
	DecayHalfLifeDays int     `yaml:"decay_half_life_days,omitempty"` // default: 7
}

// EgressRef configures egress security controls.
type EgressRef struct {
	Profile        string   `yaml:"profile,omitempty"` // strict, standard, permissive
	Mode           string   `yaml:"mode,omitempty"`    // deny-all, allowlist, dev-open
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
	Capabilities   []string `yaml:"capabilities,omitempty"` // capability bundles (e.g., "slack", "telegram")
}

// SkillsRef references a skills definition file.
type SkillsRef struct {
	Path string `yaml:"path,omitempty"` // default: "SKILL.md"
}

// ModelRef identifies the model an agent uses.
type ModelRef struct {
	Provider  string          `yaml:"provider"`
	Name      string          `yaml:"name"`
	Version   string          `yaml:"version,omitempty"`
	Fallbacks []ModelFallback `yaml:"fallbacks,omitempty"`
}

// ModelFallback identifies an alternative LLM provider for fallback.
type ModelFallback struct {
	Provider string `yaml:"provider"`
	Name     string `yaml:"name,omitempty"`
}

// ToolRef is a lightweight reference to a tool in forge.yaml.
type ToolRef struct {
	Name   string         `yaml:"name"`
	Type   string         `yaml:"type,omitempty"`
	Config map[string]any `yaml:"config,omitempty"`
}

// ParseForgeConfig parses raw YAML bytes into a ForgeConfig and validates required fields.
func ParseForgeConfig(data []byte) (*ForgeConfig, error) {
	var cfg ForgeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing forge config: %w", err)
	}

	if cfg.AgentID == "" {
		return nil, fmt.Errorf("forge config: agent_id is required")
	}
	if cfg.Version == "" {
		return nil, fmt.Errorf("forge config: version is required")
	}
	if cfg.Entrypoint == "" {
		return nil, fmt.Errorf("forge config: entrypoint is required")
	}

	return &cfg, nil
}
