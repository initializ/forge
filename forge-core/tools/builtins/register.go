package builtins

import "github.com/initializ/forge/forge-core/tools"

// All returns all built-in tools.
func All() []tools.Tool {
	return []tools.Tool{
		&httpRequestTool{},
		&jsonParseTool{},
		&csvParseTool{},
		&datetimeNowTool{},
		&uuidGenerateTool{},
		&mathCalculateTool{},
		&webSearchTool{},
	}
}

// RegisterAll registers all built-in tools with the given registry.
func RegisterAll(reg *tools.Registry) error {
	for _, t := range All() {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// GetByName returns a built-in tool by name, or nil if not found.
func GetByName(name string) tools.Tool {
	for _, t := range All() {
		if t.Name() == name {
			return t
		}
	}
	return nil
}
