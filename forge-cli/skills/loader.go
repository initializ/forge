package skills

import (
	"os"

	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/parser"
)

// ParseFile reads a skills.md file and extracts structured SkillEntry values.
func ParseFile(path string) ([]contract.SkillEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parser.Parse(f)
}

// ParseFileWithMetadata reads a skills.md file and extracts entries with frontmatter metadata.
func ParseFileWithMetadata(path string) ([]contract.SkillEntry, *contract.SkillMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	return parser.ParseWithMetadata(f)
}
