// Package compiler converts parsed SkillEntry values into CompiledSkills.
package compiler

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
)

// Compile converts parsed SkillEntry values into CompiledSkills.
//
// A SKILL.md with N `## Tool:` sections parses into N SkillEntry
// values that all share the same Body (the body is the trailing
// markdown after the frontmatter, not per-tool). Without dedup, the
// body lands in cs.Prompt N times — the bundled code-review-github
// skill (4 tools) produced 4× repeats; aibuilderdemo's prompt.txt was
// 1199 lines for what should be ~250 (issue #147). The seen-bodies
// set emits each body once. Per-skill JSON entries still carry the
// full Body because consumers of the in-memory CompiledSkill (e.g.
// forgecore SDK) may need it for non-prompt purposes.
func Compile(entries []contract.SkillEntry) (*contract.CompiledSkills, error) {
	cs := &contract.CompiledSkills{
		Skills:  make([]contract.CompiledSkill, 0, len(entries)),
		Version: "agentskills-v1",
	}

	var promptBuilder strings.Builder
	promptBuilder.WriteString("# Available Skills\n\n")

	seenBodies := make(map[string]bool)

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
		if e.Body != "" && !seenBodies[e.Body] {
			fmt.Fprintf(&promptBuilder, "\n%s\n", e.Body)
			seenBodies[e.Body] = true
		}
		promptBuilder.WriteString("\n")
	}

	cs.Count = len(cs.Skills)
	cs.Prompt = promptBuilder.String()
	return cs, nil
}
