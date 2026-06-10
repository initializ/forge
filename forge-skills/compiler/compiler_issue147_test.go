package compiler

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

// TestCompile_BodyDeduplication_MultipleToolsInOneSkillFile pins the
// fix for issue #147 — a SKILL.md with N `## Tool:` sections parses
// into N SkillEntry values that all share the same Body. Pre-fix the
// body landed in cs.Prompt N times. The bundled code-review-github
// skill has 4 tools and produced 4× repeats; aibuilderdemo's
// prompt.txt was 1199 lines for what should be ~250.
func TestCompile_BodyDeduplication_MultipleToolsInOneSkillFile(t *testing.T) {
	sharedBody := "# Code Review Skill\n\nDetailed instructions and examples...\n"
	entries := []contract.SkillEntry{
		{Name: "code_review_diff", Description: "Diff review", Body: sharedBody},
		{Name: "code_review_file", Description: "File review", Body: sharedBody},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// The body marker should appear exactly once in the prompt — not
	// once per tool entry.
	occurrences := strings.Count(cs.Prompt, "Detailed instructions and examples")
	if occurrences != 1 {
		t.Errorf("shared body should be emitted exactly once, got %d occurrences. Prompt:\n%s",
			occurrences, cs.Prompt)
	}

	// Both tool names must still appear (only the body dedupes).
	if !strings.Contains(cs.Prompt, "code_review_diff") || !strings.Contains(cs.Prompt, "code_review_file") {
		t.Errorf("both tool names must appear in prompt catalog; got:\n%s", cs.Prompt)
	}

	// Per-skill JSON entries still carry the full Body — SDK consumers
	// of CompiledSkills may need it for non-prompt purposes.
	for i, sk := range cs.Skills {
		if sk.Body != sharedBody {
			t.Errorf("Skills[%d].Body = %q, want full shared body", i, sk.Body)
		}
	}
}

// TestCompile_BodyDeduplication_DistinctBodiesPreserved is the
// over-collapse guard: bodies that differ must both appear in the
// prompt. Only EXACT identical bodies dedup.
func TestCompile_BodyDeduplication_DistinctBodiesPreserved(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "tool_a", Description: "A", Body: "Body for skill A"},
		{Name: "tool_b", Description: "B", Body: "Body for skill B"},
	}
	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !strings.Contains(cs.Prompt, "Body for skill A") {
		t.Errorf("body A must appear; got:\n%s", cs.Prompt)
	}
	if !strings.Contains(cs.Prompt, "Body for skill B") {
		t.Errorf("body B must appear; got:\n%s", cs.Prompt)
	}
}

// TestCompile_BodyDeduplication_RealisticCodeReviewShape mirrors the
// bundled code-review-github skill (4 tools sharing one ~50-line
// body). Pre-fix the body landed in prompt.txt 4 times; this test
// guards the runtime against regressing to that shape.
func TestCompile_BodyDeduplication_RealisticCodeReviewShape(t *testing.T) {
	body := strings.Repeat("Detailed skill instructions line.\n", 50)
	entries := []contract.SkillEntry{
		{Name: "review_github_list_prs", Description: "List PRs", Body: body},
		{Name: "review_github_post_comments", Description: "Post comments", Body: body},
		{Name: "review_github_apply_labels", Description: "Apply labels", Body: body},
		{Name: "review_github_auto_merge", Description: "Auto-merge", Body: body},
	}
	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Body has 50 identical lines. Pre-fix it would appear 4× → 200
	// lines of body content. Post-fix it appears 1× → 50 lines.
	lineCount := strings.Count(cs.Prompt, "Detailed skill instructions line.")
	if lineCount != 50 {
		t.Errorf("expected body to appear once (50 lines); got %d lines. "+
			"That implies %dx duplication.", lineCount, lineCount/50)
	}
}
