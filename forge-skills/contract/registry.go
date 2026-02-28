// Package contract defines the interfaces and types shared across forge-skills subpackages.
package contract

// SkillRegistry provides read access to a collection of skill descriptors.
type SkillRegistry interface {
	// List returns all available skill descriptors.
	List() ([]SkillDescriptor, error)

	// Get returns the descriptor for the named skill, or nil if not found.
	Get(name string) *SkillDescriptor

	// LoadContent reads the full SKILL.md content for the named skill.
	LoadContent(name string) ([]byte, error)

	// HasScript reports whether the named skill has an associated script.
	HasScript(name string) bool

	// LoadScript reads the script content for the named skill.
	LoadScript(name string) ([]byte, error)

	// ListScripts returns the filenames of all scripts for the named skill.
	ListScripts(name string) []string
}
