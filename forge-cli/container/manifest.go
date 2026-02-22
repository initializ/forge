package container

import (
	"encoding/json"
	"fmt"
	"os"
)

// ImageManifest records metadata about a built container image.
type ImageManifest struct {
	AgentID  string `json:"agent_id"`
	Version  string `json:"version"`
	ImageTag string `json:"image_tag"`
	Builder  string `json:"builder"`
	Platform string `json:"platform,omitempty"`
	BuiltAt  string `json:"built_at"`
	BuildDir string `json:"build_dir"`
	Pushed   bool   `json:"pushed"`

	// Container packaging extensions
	ForgeVersion         string         `json:"forge_version,omitempty"`
	ToolInterfaceVersion string         `json:"tool_interface_version,omitempty"`
	SkillsSpecVersion    string         `json:"skills_spec_version,omitempty"`
	SkillsCount          int            `json:"skills_count,omitempty"`
	EgressProfile        string         `json:"egress_profile,omitempty"`
	EgressMode           string         `json:"egress_mode,omitempty"`
	AllowedDomainsCount  int            `json:"allowed_domains_count,omitempty"`
	DevBuild             bool           `json:"dev_build,omitempty"`
	ToolCategories       map[string]int `json:"tool_categories,omitempty"`
}

// WriteManifest writes the image manifest as JSON to the given path.
func WriteManifest(path string, m *ImageManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling image manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing image manifest: %w", err)
	}
	return nil
}

// ReadManifest reads an image manifest from the given path.
func ReadManifest(path string) (*ImageManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading image manifest: %w", err)
	}
	var m ImageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing image manifest: %w", err)
	}
	return &m, nil
}
