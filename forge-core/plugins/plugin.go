// Package plugins provides a plugin registry and hook system for Forge.
package plugins

import (
	"context"
	"fmt"
	"sync"
)

// HookPoint identifies when a plugin hook fires.
type HookPoint string

const (
	HookPreBuild  HookPoint = "pre-build"
	HookPostBuild HookPoint = "post-build"
	HookPrePush   HookPoint = "pre-push"
	HookPostPush  HookPoint = "post-push"
)

// Plugin is the interface that Forge plugins must implement.
type Plugin interface {
	// Name returns the unique plugin name.
	Name() string
	// Version returns the plugin version.
	Version() string
	// Init initialises the plugin with arbitrary configuration.
	Init(config map[string]any) error
	// Hooks returns the set of hook points this plugin wants to intercept.
	Hooks() []HookPoint
	// Execute runs the plugin logic for the given hook point.
	Execute(ctx context.Context, hook HookPoint, data map[string]any) error
}

// Registry stores registered plugins and provides lookup.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewRegistry creates an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]Plugin)}
}

// Register adds a plugin to the registry. It returns an error if a plugin
// with the same name is already registered.
func (r *Registry) Register(p Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[p.Name()]; exists {
		return fmt.Errorf("plugin %q already registered", p.Name())
	}
	r.plugins[p.Name()] = p
	return nil
}

// Get returns a plugin by name, or nil if not found.
func (r *Registry) Get(name string) Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.plugins[name]
}

// ToolDefinition describes a tool discovered by a framework plugin.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// IdentityConfig holds agent identity metadata extracted by a plugin.
type IdentityConfig struct {
	Role      string
	Goal      string
	Backstory string
}

// PluginModelConfig holds model info extracted by a plugin.
type PluginModelConfig struct {
	Provider string
	Name     string
	Version  string
}

// AgentConfig is the intermediate representation from a FrameworkPlugin.
type AgentConfig struct {
	Name        string
	Description string
	Tools       []ToolDefinition
	Identity    *IdentityConfig
	Model       *PluginModelConfig
	Extra       map[string]any
}

// FrameworkPlugin adapts a specific agent framework to the Forge build pipeline.
type FrameworkPlugin interface {
	Name() string
	DetectProject(dir string) (bool, error)
	ExtractAgentConfig(dir string) (*AgentConfig, error)
	GenerateWrapper(config *AgentConfig) ([]byte, error)
	RuntimeDependencies() []string
}
