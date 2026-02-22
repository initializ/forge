package plugins

import "fmt"

// FrameworkRegistry holds framework plugins in registration order.
type FrameworkRegistry struct {
	plugins []FrameworkPlugin
}

// NewFrameworkRegistry creates an empty FrameworkRegistry.
func NewFrameworkRegistry() *FrameworkRegistry {
	return &FrameworkRegistry{}
}

// Register appends a framework plugin to the registry.
func (r *FrameworkRegistry) Register(p FrameworkPlugin) {
	r.plugins = append(r.plugins, p)
}

// Detect iterates plugins in registration order and returns the first
// whose DetectProject returns true. Returns nil if no plugin matches.
func (r *FrameworkRegistry) Detect(dir string) (FrameworkPlugin, error) {
	for _, p := range r.plugins {
		ok, err := p.DetectProject(dir)
		if err != nil {
			return nil, fmt.Errorf("plugin %s detect: %w", p.Name(), err)
		}
		if ok {
			return p, nil
		}
	}
	return nil, nil
}

// Get returns a plugin by name, or nil if not found.
func (r *FrameworkRegistry) Get(name string) FrameworkPlugin {
	for _, p := range r.plugins {
		if p.Name() == name {
			return p
		}
	}
	return nil
}
