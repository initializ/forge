// Package custom provides the fallback framework plugin for forge/custom agent projects.
package custom

import "github.com/initializ/forge/forge-core/plugins"

// Plugin is the forge/custom fallback framework plugin.
// It handles both "forge" and "custom" framework values.
type Plugin struct{}

func (p *Plugin) Name() string { return "forge" }

// DetectProject always returns true -- forge is the fallback.
func (p *Plugin) DetectProject(dir string) (bool, error) { return true, nil }

// ExtractAgentConfig returns an empty config -- forge.yaml is the authority for forge projects.
func (p *Plugin) ExtractAgentConfig(dir string) (*plugins.AgentConfig, error) {
	return &plugins.AgentConfig{}, nil
}

// GenerateWrapper returns nil -- forge projects use the built-in LLM executor.
func (p *Plugin) GenerateWrapper(config *plugins.AgentConfig) ([]byte, error) {
	return nil, nil
}

// RuntimeDependencies returns nil -- no framework-specific dependencies.
func (p *Plugin) RuntimeDependencies() []string { return nil }
