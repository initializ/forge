package channels

// Registry holds registered channel plugins keyed by name.
type Registry struct {
	plugins map[string]ChannelPlugin
}

// NewRegistry creates an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]ChannelPlugin)}
}

// Register adds a plugin to the registry, keyed by its Name().
func (r *Registry) Register(p ChannelPlugin) {
	r.plugins[p.Name()] = p
}

// Get returns the plugin with the given name, or nil if not found.
func (r *Registry) Get(name string) ChannelPlugin {
	return r.plugins[name]
}
