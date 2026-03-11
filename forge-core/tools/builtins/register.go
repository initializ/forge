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
		&fileCreateTool{},
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

// CodeAgentSearchTools returns search/exploration tools (grep, glob, tree).
// These are safe read-only tools for exploring codebases.
func CodeAgentSearchTools(workDir string) []tools.Tool {
	pv := NewPathValidator(workDir)
	return []tools.Tool{
		&grepSearchTool{pathValidator: pv},
		&globSearchTool{pathValidator: pv},
		&directoryTreeTool{pathValidator: pv},
	}
}

// RegisterCodeAgentSearchTools registers search/exploration tools.
func RegisterCodeAgentSearchTools(reg *tools.Registry, workDir string) error {
	for _, t := range CodeAgentSearchTools(workDir) {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// CodeAgentReadTools returns read-only coding tools (file_read + search).
func CodeAgentReadTools(workDir string) []tools.Tool {
	pv := NewPathValidator(workDir)
	return []tools.Tool{
		&fileReadTool{pathValidator: pv},
		&grepSearchTool{pathValidator: pv},
		&globSearchTool{pathValidator: pv},
		&directoryTreeTool{pathValidator: pv},
	}
}

// CodeAgentWriteTools returns write/execute tools.
func CodeAgentWriteTools(workDir string) []tools.Tool {
	pv := NewPathValidator(workDir)
	return []tools.Tool{
		&fileWriteTool{pathValidator: pv},
		&fileEditTool{pathValidator: pv},
		&filePatchTool{pathValidator: pv},
	}
}

// CodeAgentTools returns all coding agent tools (read + write).
func CodeAgentTools(workDir string) []tools.Tool {
	return append(CodeAgentReadTools(workDir), CodeAgentWriteTools(workDir)...)
}

// RegisterCodeAgentReadTools registers only the read-only coding tools.
func RegisterCodeAgentReadTools(reg *tools.Registry, workDir string) error {
	for _, t := range CodeAgentReadTools(workDir) {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// RegisterCodeAgentWriteTools registers the write/execute coding tools.
func RegisterCodeAgentWriteTools(reg *tools.Registry, workDir string) error {
	for _, t := range CodeAgentWriteTools(workDir) {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// RegisterCodeAgentTools registers all coding agent tools with the given registry.
func RegisterCodeAgentTools(reg *tools.Registry, workDir string) error {
	for _, t := range CodeAgentTools(workDir) {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}
