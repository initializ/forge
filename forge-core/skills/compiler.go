package skills

import (
	"fmt"
	"strings"
)

// CompiledSkills holds the result of compiling skill entries.
type CompiledSkills struct {
	Skills  []CompiledSkill `json:"skills"`
	Count   int             `json:"count"`
	Version string          `json:"version"`
	Prompt  string          `json:"-"` // written separately as prompt.txt
}

// CompiledSkill represents a single compiled skill.
type CompiledSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSpec   string `json:"input_spec,omitempty"`
	OutputSpec  string `json:"output_spec,omitempty"`
}

// Compile converts parsed SkillEntry values into CompiledSkills.
func Compile(entries []SkillEntry) (*CompiledSkills, error) {
	cs := &CompiledSkills{
		Skills:  make([]CompiledSkill, 0, len(entries)),
		Version: "agentskills-v1",
	}

	var promptBuilder strings.Builder
	promptBuilder.WriteString("# Available Skills\n\n")

	for _, e := range entries {
		skill := CompiledSkill{
			Name:        e.Name,
			Description: e.Description,
			InputSpec:   e.InputSpec,
			OutputSpec:  e.OutputSpec,
		}
		cs.Skills = append(cs.Skills, skill)

		// Build prompt catalog entry
		fmt.Fprintf(&promptBuilder, "## %s\n", e.Name)
		if e.Description != "" {
			fmt.Fprintf(&promptBuilder, "%s\n", e.Description)
		}
		if e.InputSpec != "" {
			fmt.Fprintf(&promptBuilder, "Input: %s\n", e.InputSpec)
		}
		if e.OutputSpec != "" {
			fmt.Fprintf(&promptBuilder, "Output: %s\n", e.OutputSpec)
		}
		promptBuilder.WriteString("\n")
	}

	cs.Count = len(cs.Skills)
	cs.Prompt = promptBuilder.String()
	return cs, nil
}
