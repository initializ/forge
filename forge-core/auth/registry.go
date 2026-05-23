package auth

import (
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a Provider from a freeform settings map (typically
// the `settings` block of an `auth.providers[]` entry in forge.yaml).
//
// A Factory should:
//   - Unmarshal settings into its typed Config via UnmarshalSettings.
//   - Call Config.Validate() and return clear errors.
//   - Return ErrProviderNotConfigured for missing required fields.
//
// A Factory must not perform network I/O — Verify() does that lazily.
type Factory func(settings map[string]any) (Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a provider factory under the given type name. Intended to be
// called from package init() in each provider subpackage. Panics on duplicate
// registration — a duplicate is a programming error that must fail loud at
// startup, not at the first request.
func Register(typeName string, factory Factory) {
	if typeName == "" {
		panic("auth: Register called with empty typeName")
	}
	if factory == nil {
		panic("auth: Register called with nil factory: " + typeName)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[typeName]; exists {
		panic("auth: provider already registered: " + typeName)
	}
	registry[typeName] = factory
}

// Build constructs a Provider for the given type name using the registered
// factory. Returns an error if the type is not registered.
func Build(typeName string, settings map[string]any) (Provider, error) {
	registryMu.RLock()
	factory, ok := registry[typeName]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("auth: unknown provider type %q (registered: %v)", typeName, RegisteredTypes())
	}
	p, err := factory(settings)
	if err != nil {
		return nil, fmt.Errorf("auth: build %q: %w", typeName, err)
	}
	return p, nil
}

// RegisteredTypes returns a sorted, deduplicated slice of registered provider
// type names. Used by config validation and the wizard meta endpoint to expose
// the set of available provider types.
func RegisteredTypes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// resetRegistryForTest clears all registered providers. Test-only helper —
// not exported in non-test builds. (Kept package-private here; tests in the
// same package can call it directly.)
func resetRegistryForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Factory{}
}
