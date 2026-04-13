// Package registry provides an embedded image registry mapping binary names to install methods.
package registry

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed image-registry.yaml
var registryData []byte

// RegistryEntry describes how to install a single binary in a container image.
type RegistryEntry struct {
	Name           string   `yaml:"-"`                         // set from map key
	Apt            string   `yaml:"apt,omitempty"`             // Debian/Ubuntu package
	Apk            string   `yaml:"apk,omitempty"`             // Alpine package
	URL            string   `yaml:"url,omitempty"`             // Direct download URL template
	DefaultVersion string   `yaml:"default_version,omitempty"` // Fallback version
	Dest           string   `yaml:"dest,omitempty"`            // Install path (default: /usr/local/bin/<name>)
	Chmod          string   `yaml:"chmod,omitempty"`           // Permission bits (default: "0755")
	Heavy          bool     `yaml:"heavy,omitempty"`           // Use companion Docker image
	Image          string   `yaml:"image,omitempty"`           // Companion image template
	RequiresUbuntu bool     `yaml:"requires_ubuntu,omitempty"` // Incompatible with Alpine
	RequiresFirst  []string `yaml:"requires_first,omitempty"`  // Dependencies
	Run            []string `yaml:"run,omitempty"`             // Custom RUN lines
}

// ImageRegistry holds the full set of known binaries and their install methods.
type ImageRegistry struct {
	entries map[string]RegistryEntry
}

var (
	defaultRegistry     *ImageRegistry
	defaultRegistryOnce sync.Once
	defaultRegistryErr  error
)

// Default returns the singleton ImageRegistry loaded from the embedded YAML.
func Default() (*ImageRegistry, error) {
	defaultRegistryOnce.Do(func() {
		defaultRegistry, defaultRegistryErr = Load(registryData)
	})
	return defaultRegistry, defaultRegistryErr
}

// Load parses registry YAML data into an ImageRegistry.
func Load(data []byte) (*ImageRegistry, error) {
	var raw struct {
		Bins map[string]RegistryEntry `yaml:"bins"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing image registry: %w", err)
	}

	// Set Name from map key
	for k, v := range raw.Bins {
		v.Name = k
		raw.Bins[k] = v
	}

	return &ImageRegistry{entries: raw.Bins}, nil
}

// Lookup returns the registry entry for a binary, if known.
func (r *ImageRegistry) Lookup(binName string) (RegistryEntry, bool) {
	e, ok := r.entries[binName]
	return e, ok
}

// All returns all registry entries.
func (r *ImageRegistry) All() map[string]RegistryEntry {
	return r.entries
}

// ExpandTemplate renders a Go template string with Version substitution.
func ExpandTemplate(tmplStr, version string) (string, error) {
	if !strings.Contains(tmplStr, "{{") {
		return tmplStr, nil
	}

	t, err := template.New("").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parsing template %q: %w", tmplStr, err)
	}

	var buf strings.Builder
	data := struct{ Version string }{Version: version}
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %q: %w", tmplStr, err)
	}
	return buf.String(), nil
}

// ResolveVersion returns the version to use: explicit > default.
func (e RegistryEntry) ResolveVersion(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return e.DefaultVersion
}
