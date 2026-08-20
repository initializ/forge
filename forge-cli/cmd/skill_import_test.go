package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a test helper that creates parent dirs and writes content.
func writeImportFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const sampleSkillMD = `---
name: jira-search
description: Search Jira issues
metadata:
  forge:
    requires:
      bins:
        - python3
      env:
        required:
          - JIRA_TOKEN
        one_of: []
        optional: []
    egress_domains:
      - api.atlassian.com
---
## Tool: jira_search

Search issues.

**Input:** query (string)
**Output:** JSON list of issues
`

// newAgentDir creates a minimal agent project (just forge.yaml) for import.
func newAgentDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeImportFile(t, filepath.Join(dir, "forge.yaml"), "agent_id: test-agent\nmodel:\n  provider: openai\negress:\n  mode: allowlist\n")
	return dir
}

func TestImportSkillFolder_FullFolder(t *testing.T) {
	srcDir := t.TempDir()
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), sampleSkillMD)
	writeImportFile(t, filepath.Join(srcDir, "scripts", "jira-search.py"), "print('hi')\n")
	writeImportFile(t, filepath.Join(srcDir, "reference", "schema.json"), `{"a":1}`)
	writeImportFile(t, filepath.Join(srcDir, "requirements.txt"), "requests==2.31.0\n")
	// junk that must NOT be vendored
	writeImportFile(t, filepath.Join(srcDir, "__pycache__", "x.pyc"), "junk")
	writeImportFile(t, filepath.Join(srcDir, ".venv", "lib", "y.py"), "junk")

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir})
	if err != nil {
		t.Fatalf("ImportSkillFolder error: %v", err)
	}

	if res.SkillName != "jira-search" {
		t.Errorf("SkillName = %q, want jira-search (from frontmatter)", res.SkillName)
	}

	skillDir := filepath.Join(agentDir, "skills", "jira-search")
	// SKILL.md vendored.
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not vendored: %v", err)
	}
	// Script vendored under scripts/ and executable.
	scriptPath := filepath.Join(skillDir, "scripts", "jira-search.py")
	fi, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("script not vendored: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("script mode = %v, want executable", fi.Mode().Perm())
	}
	// Reference file vendored preserving subpath.
	if _, err := os.Stat(filepath.Join(skillDir, "reference", "schema.json")); err != nil {
		t.Errorf("reference file not vendored at reference/schema.json: %v", err)
	}
	// requirements.txt vendored (as a reference).
	if _, err := os.Stat(filepath.Join(skillDir, "requirements.txt")); err != nil {
		t.Errorf("requirements.txt not vendored: %v", err)
	}
	// Junk excluded.
	if _, err := os.Stat(filepath.Join(skillDir, "__pycache__")); err == nil {
		t.Error("__pycache__ should not be vendored")
	}
	if _, err := os.Stat(filepath.Join(skillDir, ".venv")); err == nil {
		t.Error(".venv should not be vendored")
	}

	// Egress merged into forge.yaml.
	fy, _ := os.ReadFile(filepath.Join(agentDir, "forge.yaml"))
	if !strings.Contains(string(fy), "api.atlassian.com") {
		t.Errorf("egress domain not merged into forge.yaml:\n%s", fy)
	}

	// Env requirement reported missing.
	foundJira := false
	for _, e := range res.EnvMissing {
		if e.Name == "JIRA_TOKEN" {
			foundJira = true
		}
	}
	if !foundJira {
		t.Errorf("JIRA_TOKEN not reported missing: %+v", res.EnvMissing)
	}

	// Python detected, requirements.txt flagged.
	if !res.PythonDetected {
		t.Error("PythonDetected = false, want true")
	}
	if !res.RequirementsTxt {
		t.Error("RequirementsTxt = false, want true")
	}
	// requirements.txt note present (D1 wired: build installs it).
	hasReqNote := false
	for _, n := range res.Notes {
		if strings.Contains(n, "requirements.txt") {
			hasReqNote = true
		}
	}
	if !hasReqNote {
		t.Errorf("expected a requirements.txt note, got warnings=%v notes=%v", res.Warnings, res.Notes)
	}
}

func TestImportSkillFolder_FlatScriptsMovedUnderScripts(t *testing.T) {
	srcDir := t.TempDir()
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), sampleSkillMD)
	// A script at the folder root (not under scripts/).
	writeImportFile(t, filepath.Join(srcDir, "helper.py"), "print(1)\n")

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "skills", "jira-search", "scripts", "helper.py")); err != nil {
		t.Errorf("root-level script not moved under scripts/: %v", err)
	}
	if len(res.Scripts) != 1 {
		t.Errorf("Scripts = %v, want 1", res.Scripts)
	}
}

func TestImportSkillFolder_NameOverrideAndFallback(t *testing.T) {
	// Frontmatter without a name → folder basename fallback.
	noName := "---\ndescription: x\n---\n## Tool: t\n**Input:** a (string)\n"
	srcDir := filepath.Join(t.TempDir(), "My Cool Skill")
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), noName)

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir})
	if err != nil {
		t.Fatalf("fallback-name import failed: %v", err)
	}
	if res.SkillName != "my-cool-skill" {
		t.Errorf("SkillName = %q, want my-cool-skill (sanitized folder)", res.SkillName)
	}

	// Explicit override wins.
	agentDir2 := newAgentDir(t)
	res2, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir2, NameOverride: "custom-name"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.SkillName != "custom-name" {
		t.Errorf("SkillName = %q, want custom-name", res2.SkillName)
	}

	// Invalid override rejected.
	if _, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: newAgentDir(t), NameOverride: "Bad_Name"}); err == nil {
		t.Error("expected error for non-kebab --name override")
	}
}

func TestImportSkillFolder_OverwriteGuard(t *testing.T) {
	srcDir := t.TempDir()
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), sampleSkillMD)
	writeImportFile(t, filepath.Join(srcDir, "scripts", "jira-search.py"), "v1\n")
	agentDir := newAgentDir(t)

	if _, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir}); err != nil {
		t.Fatal(err)
	}
	// Second import without overwrite → error.
	if _, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir}); err == nil {
		t.Error("expected error importing over existing skill without --overwrite")
	}
	// With overwrite: stale scripts cleared, new content written.
	writeImportFile(t, filepath.Join(srcDir, "scripts", "jira-search.py"), "v2\n")
	if err := os.Remove(filepath.Join(srcDir, "scripts", "jira-search.py")); err == nil {
		// replace with a differently-named script to prove stale removal
		writeImportFile(t, filepath.Join(srcDir, "scripts", "renamed.py"), "v2\n")
	}
	if _, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir, Overwrite: true}); err != nil {
		t.Fatalf("overwrite import failed: %v", err)
	}
	skillDir := filepath.Join(agentDir, "skills", "jira-search")
	if _, err := os.Stat(filepath.Join(skillDir, "scripts", "jira-search.py")); err == nil {
		t.Error("stale script jira-search.py should have been removed on overwrite")
	}
	if _, err := os.Stat(filepath.Join(skillDir, "scripts", "renamed.py")); err != nil {
		t.Errorf("new script renamed.py not present after overwrite: %v", err)
	}
}

func TestImportSkillFolder_Errors(t *testing.T) {
	// Missing SKILL.md.
	empty := t.TempDir()
	if _, err := ImportSkillFolder(SkillImportOptions{SourceDir: empty, AgentDir: newAgentDir(t)}); err == nil {
		t.Error("expected error when source has no SKILL.md")
	}
	// Missing forge.yaml in target.
	srcDir := t.TempDir()
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), sampleSkillMD)
	if _, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: t.TempDir()}); err == nil {
		t.Error("expected error when target has no forge.yaml")
	}
}

// TestImportSkillFolder_RejectsTraversalName is the security regression: a
// SKILL.md whose frontmatter `name` contains path traversal must be rejected,
// never used to derive a skill directory outside skills/. Covers arbitrary
// write (MkdirAll+WriteFile) and, with --overwrite, arbitrary delete.
func TestImportSkillFolder_RejectsTraversalName(t *testing.T) {
	agentDir := newAgentDir(t)
	// A sentinel directory outside the project that must never be touched.
	victim := filepath.Join(t.TempDir(), "victim")
	writeImportFile(t, filepath.Join(victim, "keep.txt"), "do not delete")

	for _, evil := range []string{
		"../../../../etc",
		"..",
		"../outside",
		"foo/bar",  // slash → not kebab
		"Bad_Name", // underscore/upper → not kebab
	} {
		srcDir := t.TempDir()
		md := "---\nname: " + evil + "\ndescription: x\n---\n## Tool: t\n**Input:** a (string)\n"
		writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), md)

		// Without and with overwrite — both must error, nothing written/deleted.
		for _, ow := range []bool{false, true} {
			_, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir, Overwrite: ow})
			if err == nil {
				t.Errorf("expected rejection for traversal/non-kebab name %q (overwrite=%v)", evil, ow)
			}
		}
	}
	// The victim is untouched.
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Errorf("victim file was affected by a traversal import: %v", err)
	}
	// Nothing escaped into the parent of skills/.
	if entries, _ := os.ReadDir(filepath.Join(agentDir, "skills")); len(entries) != 0 {
		t.Errorf("skills/ should be empty after rejected imports, got %d entries", len(entries))
	}
}

// TestImportSkillFolder_SkipsSymlinks ensures a symlinked file in the source is
// not vendored (its target content would otherwise be smuggled in).
func TestImportSkillFolder_SkipsSymlinks(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "id_rsa")
	writeImportFile(t, secret, "TOP SECRET KEY")

	srcDir := t.TempDir()
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), sampleSkillMD)
	if err := os.MkdirAll(filepath.Join(srcDir, "reference"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(srcDir, "reference", "leaked.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "skills", "jira-search", "reference", "leaked.txt")); err == nil {
		t.Error("symlink was vendored — host content smuggling not prevented")
	}
	hasWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "symlink") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected a skipped-symlink warning, got %v", res.Warnings)
	}
}

// TestImportSkillFolder_ScriptCollisionKeepsFirst ensures two root-level scripts
// with the same basename don't silently clobber — the first wins and a warning
// is emitted.
func TestImportSkillFolder_ScriptCollisionKeepsFirst(t *testing.T) {
	srcDir := t.TempDir()
	writeImportFile(t, filepath.Join(srcDir, "SKILL.md"), sampleSkillMD)
	writeImportFile(t, filepath.Join(srcDir, "a", "run.sh"), "echo A\n")
	writeImportFile(t, filepath.Join(srcDir, "b", "run.sh"), "echo B\n")

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: srcDir, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(agentDir, "skills", "jira-search", "scripts", "run.sh"))
	if string(got) != "echo A\n" {
		t.Errorf("collision clobbered the first script: got %q, want the first (echo A)", got)
	}
	hasWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "collision") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected a script-collision warning, got %v", res.Warnings)
	}
}

func TestClassifyImportFile(t *testing.T) {
	cases := []struct {
		in         string
		wantScript bool
		wantDest   string
	}{
		{"scripts/run.py", true, "scripts/run.py"},
		{"scripts/nested/run.sh", true, "scripts/nested/run.sh"},
		{"helper.py", true, "scripts/helper.py"},
		{"reference/schema.json", false, "reference/schema.json"},
		{"README.md", false, "README.md"},
		{"scripts/data.json", false, "scripts/data.json"}, // non-script under scripts/ stays put, not executable
	}
	for _, c := range cases {
		gotScript, gotDest := classifyImportFile(c.in)
		if gotScript != c.wantScript || gotDest != c.wantDest {
			t.Errorf("classifyImportFile(%q) = (%v,%q), want (%v,%q)", c.in, gotScript, gotDest, c.wantScript, c.wantDest)
		}
	}
}

func TestSanitizeToKebab(t *testing.T) {
	cases := map[string]string{
		"My Cool Skill":   "my-cool-skill",
		"jira_search":     "jira-search",
		"  Weird--Name  ": "weird-name",
		"UPPER":           "upper",
	}
	for in, want := range cases {
		if got := sanitizeToKebab(in); got != want {
			t.Errorf("sanitizeToKebab(%q) = %q, want %q", in, got, want)
		}
	}
}
