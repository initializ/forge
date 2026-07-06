package builtins

import (
	"github.com/initializ/forge/forge-core/credentials"
	"github.com/initializ/forge/forge-core/tools"
)

// Options configures which optional per-tool integrations are wired
// when constructing the builtin tools. Governance R9 uses this to
// attach the JIT credential injector to http_request; future options
// (per-tool tracing, per-tool rate limits) belong here too so the
// public All() / RegisterAll() surface stays a small set of variadic
// entry points.
type Options struct {
	// HTTPCredentialInjector, when non-nil, is wired into the
	// http_request tool so per-call JIT credentials become request
	// headers. Zero value → no JIT injection.
	HTTPCredentialInjector *credentials.Injector
}

// All returns all built-in tools. Accepts zero or one Options
// value; extra Options are ignored (variadic for backward-compat
// only — callers should pass at most one).
func All(opts ...Options) []tools.Tool {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return []tools.Tool{
		(&httpRequestTool{}).WithCredentialInjector(o.HTTPCredentialInjector),
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
// Same variadic-Options convention as All(); pass at most one.
func RegisterAll(reg *tools.Registry, opts ...Options) error {
	for _, t := range All(opts...) {
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
