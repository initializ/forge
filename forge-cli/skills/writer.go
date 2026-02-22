package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	coreskills "github.com/initializ/forge/forge-core/skills"
)

// WriteArtifacts creates compiled/skills/skills.json and compiled/prompt.txt in outputDir.
func WriteArtifacts(outputDir string, cs *coreskills.CompiledSkills) error {
	skillsDir := filepath.Join(outputDir, "compiled", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return fmt.Errorf("creating skills directory: %w", err)
	}

	// Write skills.json
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling skills: %w", err)
	}
	skillsPath := filepath.Join(skillsDir, "skills.json")
	if err := os.WriteFile(skillsPath, data, 0644); err != nil {
		return fmt.Errorf("writing skills.json: %w", err)
	}

	// Write prompt.txt
	compiledDir := filepath.Join(outputDir, "compiled")
	promptPath := filepath.Join(compiledDir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(cs.Prompt), 0644); err != nil {
		return fmt.Errorf("writing prompt.txt: %w", err)
	}

	return nil
}
