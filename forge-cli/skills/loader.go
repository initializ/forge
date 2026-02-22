package skills

import (
	"os"

	coreskills "github.com/initializ/forge/forge-core/skills"
)

// ParseFile reads a skills.md file and extracts structured SkillEntry values.
func ParseFile(path string) ([]coreskills.SkillEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return coreskills.Parse(f)
}

// ParseFileWithMetadata reads a skills.md file and extracts entries with frontmatter metadata.
func ParseFileWithMetadata(path string) ([]coreskills.SkillEntry, *coreskills.SkillMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return coreskills.ParseWithMetadata(f)
}
