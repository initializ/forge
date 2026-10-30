package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Namespace names under the global memory root (~/.forge/memory).
const (
	// NamespaceProjects holds coding-session memory keyed by ProjectID
	// (written by the optimizer proxy): the 3-tier card system.
	NamespaceProjects = "projects"
	// NamespaceAgents holds deployed-agent operational memory keyed by
	// AgentID (written by the agent runtime).
	NamespaceAgents = "agents"
)

// RegistryEntry records the identity mapping for one project or agent. For
// projects, Source is the normalized git remote; for agents it is the raw
// forge.yaml agent_id. LocalPaths accumulates every worktree/workdir observed
// for this id so two checkouts of one remote collapse to a single entry.
type RegistryEntry struct {
	ID         string    `json:"id"`
	Source     string    `json:"source,omitempty"`
	Name       string    `json:"name,omitempty"`
	LocalPaths []string  `json:"local_paths,omitempty"`
	LastSeen   time.Time `json:"last_seen"`
}

// Registry is a JSON-file map of id → RegistryEntry, one file per namespace
// (projects.json / agents.json) under the global memory root. It is safe for
// concurrent use within a process; the underlying file is written atomically.
type Registry struct {
	mu   sync.Mutex
	path string
}

// OpenRegistry opens (creating on first write) the registry file for a
// namespace under root, e.g. <root>/projects.json.
func OpenRegistry(root, namespace string) (*Registry, error) {
	if namespace != NamespaceProjects && namespace != NamespaceAgents {
		return nil, fmt.Errorf("invalid namespace %q", namespace)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating memory root: %w", err)
	}
	return &Registry{path: filepath.Join(root, namespace+".json")}, nil
}

// Upsert merges e into the registry: it unions LocalPaths with any existing
// entry, preserves a prior Name/Source when e leaves them blank, and stamps
// LastSeen with the current time when e leaves it zero.
func (r *Registry) Upsert(e RegistryEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, err := r.loadLocked()
	if err != nil {
		return err
	}

	if existing, ok := m[e.ID]; ok {
		e.LocalPaths = unionPaths(existing.LocalPaths, e.LocalPaths)
		if e.Name == "" {
			e.Name = existing.Name
		}
		if e.Source == "" {
			e.Source = existing.Source
		}
		if e.LastSeen.IsZero() {
			e.LastSeen = existing.LastSeen
		}
	} else {
		e.LocalPaths = unionPaths(nil, e.LocalPaths)
	}
	if e.LastSeen.IsZero() {
		e.LastSeen = time.Now().UTC()
	}

	m[e.ID] = e
	return r.saveLocked(m)
}

// Get returns the entry for id and whether it was found.
func (r *Registry) Get(id string) (RegistryEntry, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, err := r.loadLocked()
	if err != nil {
		return RegistryEntry{}, false, err
	}
	e, ok := m[id]
	return e, ok, nil
}

// List returns all entries, sorted by id for stable output.
func (r *Registry) List() ([]RegistryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, err := r.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]RegistryEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sortEntries(out)
	return out, nil
}

func (r *Registry) loadLocked() (map[string]RegistryEntry, error) {
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return map[string]RegistryEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	m := map[string]RegistryEntry{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decoding registry %s: %w", r.path, err)
	}
	return m, nil
}

func (r *Registry) saveLocked(m map[string]RegistryEntry) error {
	// json.Marshal sorts string map keys, so output is deterministic.
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding registry: %w", err)
	}
	return atomicWriteFile(r.path, data, 0o644)
}

// unionPaths returns the union of two path slices with order preserved
// (existing first, then any new paths) and duplicates removed.
func unionPaths(existing, add []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(add))
	var out []string
	for _, p := range existing {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for _, p := range add {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// sortEntries sorts entries by ID in place.
func sortEntries(entries []RegistryEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].ID < entries[j-1].ID; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
