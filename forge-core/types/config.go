// Package types holds configuration types for forge.yaml.
package types

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ForgeConfig represents the top-level forge.yaml configuration.
type ForgeConfig struct {
	AgentID    string    `yaml:"agent_id"`
	Version    string    `yaml:"version"`
	Framework  string    `yaml:"framework"`
	Entrypoint string    `yaml:"entrypoint"`
	Model      ModelRef  `yaml:"model,omitempty"`
	Tools      []ToolRef `yaml:"tools,omitempty"`
	Channels   []string  `yaml:"channels,omitempty"`
	Registry   string    `yaml:"registry,omitempty"`
	Egress     EgressRef `yaml:"egress,omitempty"`
	Skills     SkillsRef `yaml:"skills,omitempty"`
}

// EgressRef configures egress security controls.
type EgressRef struct {
	Profile        string   `yaml:"profile,omitempty"`         // strict, standard, permissive
	Mode           string   `yaml:"mode,omitempty"`            // deny-all, allowlist, dev-open
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
	Capabilities   []string `yaml:"capabilities,omitempty"`    // capability bundles (e.g., "slack", "telegram")
}

// SkillsRef references a skills definition file.
type SkillsRef struct {
	Path string `yaml:"path,omitempty"` // default: "skills.md"
}

// ModelRef identifies the model an agent uses.
type ModelRef struct {
	Provider string `yaml:"provider"`
	Name     string `yaml:"name"`
	Version  string `yaml:"version,omitempty"`
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
