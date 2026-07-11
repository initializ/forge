package forgeui

import (
	"strings"
	"testing"
)

// TestSkillBuilderPrompt_ConvergenceRules pins the interview/convergence
// discipline (issue #252) so it can't be silently dropped — a stuck or
// looping interview is the main UX failure mode for a chat builder.
func TestSkillBuilderPrompt_ConvergenceRules(t *testing.T) {
	p := skillBuilderSystemPrompt(modeCreate, nil)
	for _, want := range []string{
		"Converge Quickly",
		"NEVER re-ask",
		"AT MOST ONE clarifying question",
		"STOP asking and return the complete SKILL.md",
		"Prefer a sensible default",
		// The convergence trigger must count the install recipe as a
		// first-class criterion, not hide it in a parenthetical — else the
		// LLM drafts once it has task+creds+tools and invents a plausible
		// install URL/package for a missing binary (#258 review).
		"you need FOUR things",
		"install recipe for every binary the base image lacks",
		"NEVER draft with an invented package name or download URL",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("convergence prompt missing directive: %q", want)
		}
	}
}

// TestSkillBuilderPrompt_InstallRecipes pins the custom-binary install
// guidance — requires.bins supports a mapping form (apt/apk/url+dest+chmod)
// per BinRequirement, and the prompt must teach it.
func TestSkillBuilderPrompt_InstallRecipes(t *testing.T) {
	p := skillBuilderSystemPrompt(modeCreate, nil)
	for _, want := range []string{
		"install recipe",
		"apt:",
		"url:",
		"dest:",
		"chmod:",
		"NEVER invent a download URL",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("install-recipe prompt missing: %q", want)
		}
	}
}

// TestSkillBuilderPrompt_StructuredOutput pins the #252 part 2 output
// contract: the builder must instruct a single {message, skill} JSON
// envelope (skill:null while interviewing, {skill_md, scripts} once drafted)
// so the handler consumes JSON instead of fragile fence-parsing.
func TestSkillBuilderPrompt_StructuredOutput(t *testing.T) {
	p := skillBuilderSystemPrompt(modeCreate, nil)
	for _, want := range []string{
		"STRUCTURED JSON",
		"SINGLE JSON object",
		`"message"`,
		`"skill"`,
		"skill_md",
		"scripts",
		"null",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("structured-output prompt missing: %q", want)
		}
	}
}

// TestSkillBuilderPrompt_EditModeStructuredOutput pins that edit mode routes
// the updated skill through skill.skill_md and the change summary through the
// message field (not the old fence + trailing **Changed:** shape).
func TestSkillBuilderPrompt_EditModeStructuredOutput(t *testing.T) {
	p := skillBuilderSystemPrompt(modeEdit, &existingSkillContext{
		Name:    "demo",
		SkillMD: "---\nname: demo\n---\n## Tool: do_thing\n",
	})
	for _, want := range []string{
		"`skill.skill_md` field",
		"`message` field",
		"`skill.scripts`",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("edit-mode structured-output rule missing: %q", want)
		}
	}
}

// TestSkillBuilderPrompt_ForgeCorrectnessRetained guards the Forge-runtime
// rules that must survive any interview-architecture edit (#252): the $1
// JSON input contract, structured JSON output, the per-tool section shape
// the agent selects on, and python-via-bins.
func TestSkillBuilderPrompt_ForgeCorrectnessRetained(t *testing.T) {
	p := skillBuilderSystemPrompt(modeCreate, nil)
	for _, want := range []string{
		`INPUT="${1:-}"`,               // $1 input contract
		"structured JSON output",       // never raw text
		"## Tool:",                     // per-tool section
		"**Input:**",                   // input parameter table
		"**Output:**",                  // output schema
		"Examples:",                    // request -> tool input examples
		"Safety Constraints",           // safety section
		"set -euo pipefail",            // bash safety
		"add python3 to requires.bins", // python-via-bins
	} {
		if !strings.Contains(p, want) {
			t.Errorf("Forge-correctness rule dropped from prompt: %q", want)
		}
	}
}

// TestSkillBuilderPrompt_EditModePreservesToolNames guards issue #193's
// edit-mode rule: renaming a `## Tool:` heading breaks wired agents.
func TestSkillBuilderPrompt_EditModePreservesToolNames(t *testing.T) {
	p := skillBuilderSystemPrompt(modeEdit, &existingSkillContext{
		Name:    "demo",
		SkillMD: "---\nname: demo\n---\n## Tool: do_thing\n",
	})
	if !strings.Contains(p, "Preserve every `## Tool: <name>` heading exactly") {
		t.Error("edit mode dropped the tool-name-preservation rule")
	}
	if !strings.Contains(p, "Converge Quickly") {
		t.Error("edit mode should still carry the convergence rules (base prompt)")
	}
}
