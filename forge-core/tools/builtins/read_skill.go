package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

// ReadSkillTool reads a skill's SKILL.md file on demand.
// This enables lazy-loading: the LLM only loads full skill instructions
// when it decides a skill is relevant.
type ReadSkillTool struct {
	workDir string
}

// NewReadSkillTool creates a read_skill tool rooted at the given working directory.
func NewReadSkillTool(workDir string) *ReadSkillTool {
	return &ReadSkillTool{workDir: workDir}
}

func (t *ReadSkillTool) Name() string             { return "read_skill" }
func (t *ReadSkillTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *ReadSkillTool) Description() string {
	return "Read the full instructions for an available skill. " +
		"Returns the skill's SKILL.md content with usage details, parameters, and examples."
}

func (t *ReadSkillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name (e.g. 'github', 'weather')"}
		},
		"required": ["name"]
	}`)
}

func (t *ReadSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing read_skill input: %w", err)
	}
	if input.Name == "" {
		return `{"error": "name is required"}`, nil
	}

	// Security: prevent directory traversal
	if strings.Contains(input.Name, "/") || strings.Contains(input.Name, "\\") || strings.Contains(input.Name, "..") {
		return `{"error": "invalid skill name"}`, nil
	}

	// Try multiple naming conventions to find the skill file.
	// Tool names use underscores (k8s_triage) while directories use hyphens (k8s-incident-triage).
	nameVariants := []string{input.Name}
	hyphenated := strings.ReplaceAll(input.Name, "_", "-")
	if hyphenated != input.Name {
		nameVariants = append(nameVariants, hyphenated)
	}

	var data []byte
	var err error
	for _, name := range nameVariants {
		// Flat format: skills/{name}.md
		path := filepath.Join(t.workDir, "skills", name+".md")
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
		// Subdirectory format: skills/{name}/SKILL.md
		path = filepath.Join(t.workDir, "skills", name, "SKILL.md")
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf(`{"error": "skill %q not found"}`, input.Name), nil
		}
		return "", fmt.Errorf("reading skill file: %w", err)
	}

	return string(data), nil
}
