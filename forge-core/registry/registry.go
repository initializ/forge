// Package registry provides an embedded skill registry for the forge init wizard.
// Skills are embedded at compile time and can be vendored into new projects.
package registry

import (
	"embed"
	"encoding/json"
)

//go:embed skills
var skillFS embed.FS

//go:embed scripts
var scriptFS embed.FS

//go:embed index.json
var indexJSON []byte

// SkillInfo describes a skill available in the embedded registry.
type SkillInfo struct {
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	Description   string   `json:"description"`
	SkillFile     string   `json:"skill_file"`
	RequiredEnv   []string `json:"required_env,omitempty"`
	OneOfEnv      []string `json:"one_of_env,omitempty"`
	OptionalEnv   []string `json:"optional_env,omitempty"`
	RequiredBins  []string `json:"required_bins,omitempty"`
	EgressDomains []string `json:"egress_domains,omitempty"`
}

// LoadIndex parses the embedded index.json and returns all registered skills.
func LoadIndex() ([]SkillInfo, error) {
	var skills []SkillInfo
	if err := json.Unmarshal(indexJSON, &skills); err != nil {
		return nil, err
	}
	return skills, nil
}

// LoadSkillFile reads the embedded markdown file for the given skill name.
func LoadSkillFile(name string) ([]byte, error) {
	return skillFS.ReadFile("skills/" + name + ".md")
}

// GetSkillByName returns the SkillInfo for a given skill name, or nil if not found.
func GetSkillByName(name string) *SkillInfo {
	skills, err := LoadIndex()
	if err != nil {
		return nil
	}
	for i := range skills {
		if skills[i].Name == name {
			return &skills[i]
		}
	}
	return nil
}

// LoadSkillScript reads an embedded script for a skill.
func LoadSkillScript(name string) ([]byte, error) {
	return scriptFS.ReadFile("scripts/" + name + ".sh")
}

// HasSkillScript checks if a skill has an embedded script.
func HasSkillScript(name string) bool {
	_, err := scriptFS.ReadFile("scripts/" + name + ".sh")
	return err == nil
}
