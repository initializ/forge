package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRootSkill_WritesFrontmatterAndBody(t *testing.T) {
	dir := t.TempDir()
	opts := &initOptions{
		Name:         "PR Reviewer",
		AgentID:      "pr-reviewer",
		Description:  "Reviews pull requests",
		SystemPrompt: "You are PR Reviewer.\nReview diffs carefully.",
	}
	if err := writeRootSkill(dir, opts); err != nil {
		t.Fatalf("writeRootSkill: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "---\nname: pr-reviewer\n") {
		t.Errorf("missing/incorrect frontmatter: %q", s)
	}
	if !strings.Contains(s, `description: "Reviews pull requests"`) {
		t.Errorf("description not quoted/written: %q", s)
	}
	if !strings.Contains(s, "You are PR Reviewer.\nReview diffs carefully.") {
		t.Errorf("persona body missing: %q", s)
	}
}

func TestWriteRootSkill_DoesNotClobberExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("hand-edited persona"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &initOptions{Name: "X", AgentID: "x", SystemPrompt: "generated", Force: false}
	if err := writeRootSkill(dir, opts); err != nil {
		t.Fatalf("writeRootSkill: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hand-edited persona" {
		t.Errorf("existing SKILL.md clobbered: %q", got)
	}
}

func TestYAMLQuote(t *testing.T) {
	cases := map[string]string{
		`simple`:       `"simple"`,
		`has "quotes"`: `"has \"quotes\""`,
		"has\nnewline": `"has newline"`,
		`back\slash`:   `"back\\slash"`,
	}
	for in, want := range cases {
		if got := yamlQuote(in); got != want {
			t.Errorf("yamlQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteRootSkill_ForceClobbersExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("old persona"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &initOptions{Name: "X", AgentID: "x", SystemPrompt: "new persona", Force: true}
	if err := writeRootSkill(dir, opts); err != nil {
		t.Fatalf("writeRootSkill: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "new persona") {
		t.Errorf("Force should overwrite; got %q", got)
	}
}

func TestWriteRootSkill_EmptyDescriptionDefault(t *testing.T) {
	dir := t.TempDir()
	opts := &initOptions{Name: "Cool Agent", AgentID: "cool-agent", SystemPrompt: "persona"}
	if err := writeRootSkill(dir, opts); err != nil {
		t.Fatalf("writeRootSkill: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if !strings.Contains(string(got), `description: "Cool Agent agent"`) {
		t.Errorf("empty description should default to '<name> agent'; got %q", got)
	}
}
