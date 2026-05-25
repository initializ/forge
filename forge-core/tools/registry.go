package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/initializ/forge/forge-core/llm"
)

// Registry is a thread-safe tool registry. It implements engine.ToolExecutor
// via Go structural typing -- no direct import of the engine package is needed.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. Returns an error if a tool with the
// same name is already registered.
//
// Tool names containing "__" are reserved for MCP-discovered tools
// (the "<server>__<tool>" namespaced form). Non-MCP tools that try
// to use that separator are rejected — this prevents a builtin from
// accidentally shadowing an MCP tool's namespace. MCP tools must
// implement the MCPSource marker interface to opt in.
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if strings.Contains(name, "__") {
		if _, isMCP := t.(MCPSource); !isMCP {
			return fmt.Errorf("tool name %q contains '__' which is reserved for MCP namespacing (tool must implement tools.MCPSource)", name)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool already registered: %q", name)
	}
	r.tools[name] = t
	return nil
}

// Remove deletes a tool from the registry by name. No-op if not found.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Get returns the tool with the given name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// List returns the names of all registered tools, sorted alphabetically.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Execute runs the named tool with the given arguments.
// This method satisfies the engine.ToolExecutor interface.
func (r *Registry) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("unknown tool: %q", name)
	}
	return t.Execute(ctx, arguments)
}

// Filter returns a new Registry containing only tools whose names are in the allowed list.
// This is useful for Command to restrict which tools are available at runtime.
func (r *Registry) Filter(allowed []string) *Registry {
	allowSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowSet[name] = true
	}

	filtered := NewRegistry()
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, tool := range r.tools {
		if allowSet[name] {
			filtered.tools[name] = tool
		}
	}
	return filtered
}

// ToolDefinitions returns LLM tool definitions for all registered tools.
// This method satisfies the engine.ToolExecutor interface.
func (r *Registry) ToolDefinitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		defs = append(defs, ToLLMDefinition(r.tools[name]))
	}
	return defs
}
