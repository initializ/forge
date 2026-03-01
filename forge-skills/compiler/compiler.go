// Package compiler converts parsed SkillEntry values into CompiledSkills.
package compiler

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
)

// Compile converts parsed SkillEntry values into CompiledSkills.
func Compile(entries []contract.SkillEntry) (*contract.CompiledSkills, error) {
	cs := &contract.CompiledSkills{
		Skills:  make([]contract.CompiledSkill, 0, len(entries)),
		Version: "agentskills-v1",
	}

	var promptBuilder strings.Builder
	promptBuilder.WriteString("# Available Skills\n\n")

	for _, e := range entries {
		skill := contract.CompiledSkill{
			Name:         e.Name,
			Description:  e.Description,
			InputSpec:    e.InputSpec,
			OutputSpec:   e.OutputSpec,
			OutputFormat: e.OutputFormat,
			Body:         e.Body,
		}
		if e.Metadata != nil {
			skill.Category = e.Metadata.Category
			skill.Tags = e.Metadata.Tags
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
		if e.OutputFormat != "" {
			fmt.Fprintf(&promptBuilder, "Output format: %s\n", e.OutputFormat)
		}
		if e.Body != "" {
			fmt.Fprintf(&promptBuilder, "\n%s\n", e.Body)
		}
		promptBuilder.WriteString("\n")
	}

	cs.Count = len(cs.Skills)
	cs.Prompt = promptBuilder.String()
	return cs, nil
}
