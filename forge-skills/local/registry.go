package local

import (
	"fmt"
	"io/fs"

	"github.com/initializ/forge/forge-skills/contract"
)

// LocalRegistry implements contract.SkillRegistry backed by an fs.FS.
type LocalRegistry struct {
	fsys   fs.FS
	skills []contract.SkillDescriptor
	byName map[string]*contract.SkillDescriptor
}

// NewLocalRegistry creates a LocalRegistry by scanning the given filesystem.
func NewLocalRegistry(fsys fs.FS) (*LocalRegistry, error) {
	skills, err := Scan(fsys)
	if err != nil {
		return nil, fmt.Errorf("scanning skills: %w", err)
	}

	byName := make(map[string]*contract.SkillDescriptor, len(skills))
	for i := range skills {
		byName[skills[i].Name] = &skills[i]
	}

	return &LocalRegistry{
		fsys:   fsys,
		skills: skills,
		byName: byName,
	}, nil
}

// NewEmbeddedRegistry creates a LocalRegistry backed by the compile-time embedded skills.
// Embedded skills are assigned TrustBuiltin provenance.
func NewEmbeddedRegistry() (*LocalRegistry, error) {
	sub, err := fs.Sub(embeddedSkillsFS, "embedded")
	if err != nil {
		return nil, fmt.Errorf("accessing embedded skills: %w", err)
	}
	reg, err := NewLocalRegistry(sub)
	if err != nil {
		return nil, err
	}
	// Upgrade trust level for embedded skills
	for i := range reg.skills {
		if reg.skills[i].Provenance != nil {
			reg.skills[i].Provenance.Source = "embedded"
			reg.skills[i].Provenance.Trust = contract.TrustBuiltin
		}
	}
	return reg, nil
}

// List returns all available skill descriptors.
func (r *LocalRegistry) List() ([]contract.SkillDescriptor, error) {
	return r.skills, nil
}

// Get returns the descriptor for the named skill, or nil if not found.
func (r *LocalRegistry) Get(name string) *contract.SkillDescriptor {
	return r.byName[name]
}

// LoadContent reads the full SKILL.md content for the named skill.
func (r *LocalRegistry) LoadContent(name string) ([]byte, error) {
	dirName := r.dirName(name)
	return fs.ReadFile(r.fsys, dirName+"/SKILL.md")
}

// HasScript reports whether the named skill has an associated script.
func (r *LocalRegistry) HasScript(name string) bool {
	dirName := r.dirName(name)
	_, err := fs.ReadFile(r.fsys, dirName+"/scripts/"+name+".sh")
	return err == nil
}

// LoadScript reads the script content for the named skill.
func (r *LocalRegistry) LoadScript(name string) ([]byte, error) {
	dirName := r.dirName(name)
	return fs.ReadFile(r.fsys, dirName+"/scripts/"+name+".sh")
}

// ListScripts returns the filenames of all scripts for the named skill.
func (r *LocalRegistry) ListScripts(name string) []string {
	dirName := r.dirName(name)
	scriptsDir := dirName + "/scripts"
	entries, err := fs.ReadDir(r.fsys, scriptsDir)
	if err != nil {
		return nil
	}
	var scripts []string
	for _, e := range entries {
		if !e.IsDir() {
			scripts = append(scripts, e.Name())
		}
	}
	return scripts
}

// LoadScriptByName reads a specific script file for the named skill.
func (r *LocalRegistry) LoadScriptByName(name, scriptFile string) ([]byte, error) {
	dirName := r.dirName(name)
	return fs.ReadFile(r.fsys, dirName+"/scripts/"+scriptFile)
}

// dirName returns the directory name for a skill. The directory name matches
// the skill name (which is the directory name used in the embedded FS).
func (r *LocalRegistry) dirName(name string) string {
	// The directory name is the same as the skill name
	return name
}
