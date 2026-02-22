// Package custom provides the fallback framework plugin for custom agent projects.
package custom

import "github.com/initializ/forge/forge-core/plugins"

// Plugin is the custom/fallback framework plugin.
type Plugin struct{}

func (p *Plugin) Name() string { return "custom" }

// DetectProject always returns true -- custom is the fallback.
func (p *Plugin) DetectProject(dir string) (bool, error) { return true, nil }

// ExtractAgentConfig returns an empty config -- forge.yaml is the authority for custom projects.
func (p *Plugin) ExtractAgentConfig(dir string) (*plugins.AgentConfig, error) {
	return &plugins.AgentConfig{}, nil
}

// GenerateWrapper returns nil -- custom projects already include their own A2A server.
func (p *Plugin) GenerateWrapper(config *plugins.AgentConfig) ([]byte, error) {
	return nil, nil
}

// RuntimeDependencies returns nil -- no framework-specific dependencies.
func (p *Plugin) RuntimeDependencies() []string { return nil }
