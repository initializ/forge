package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill writes skills/<name>/SKILL.md and any scripts under
// skills/<name>/scripts/ into root.
func writeSkill(t *testing.T, root, name, body string, scripts map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for fname, content := range scripts {
		if err := os.WriteFile(filepath.Join(dir, "scripts", fname), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func findingFor(fs []skillToolFinding, skill, tool string) *skillToolFinding {
	for i := range fs {
		if fs[i].Skill == skill && fs[i].Tool == tool {
			return &fs[i]
		}
	}
	return nil
}

func TestLintSkillTools(t *testing.T) {
	root := t.TempDir()

	// hyphen-named script backs an underscore tool (historical convention).
	writeSkill(t, root, "hyphen", "---\nname: hyphen\ndescription: d\n---\n## Tool: do_thing\nDoes it.\n**Input:** query (string)\n",
		map[string]string{"do-thing.sh": "#!/bin/sh\n"})

	// underscore-named script backs an underscore tool (NEW: #418).
	writeSkill(t, root, "underscore", "---\nname: underscore\ndescription: d\n---\n## Tool: run_report\n**Input:** name (string)\n",
		map[string]string{"run_report.sh": "#!/bin/sh\n"})

	// invalid **Input:** property key ("pod name" has a space).
	writeSkill(t, root, "badkey", "---\nname: badkey\ndescription: d\n---\n## Tool: scan\n**Input:** pod name (string)\n",
		map[string]string{"scan.sh": "#!/bin/sh\n"})

	// tool with no backing script at all.
	writeSkill(t, root, "missing", "---\nname: missing\ndescription: d\n---\n## Tool: ghost\n**Input:** x (string)\n", nil)

	// orphan script: a scripts file no `## Tool:` claims.
	writeSkill(t, root, "orphan", "---\nname: orphan\ndescription: d\n---\n## Tool: real\n**Input:** x (string)\n",
		map[string]string{"real.sh": "#!/bin/sh\n", "leftover.sh": "#!/bin/sh\n"})

	findings := lintSkillTools(root, filepath.Join(root, "SKILL.md"))

	// Both name forms resolve → no missing-script finding for these tools.
	if f := findingFor(findings, "hyphen", "do_thing"); f != nil {
		t.Errorf("hyphen/do_thing should be backed (hyphen script), got: %s", f.Msg)
	}
	if f := findingFor(findings, "underscore", "run_report"); f != nil {
		t.Errorf("underscore/run_report should be backed (underscore script, #418), got: %s", f.Msg)
	}

	// Invalid key → error.
	if f := findingFor(findings, "badkey", "scan"); f == nil || f.Level != "error" {
		t.Errorf("expected error finding for badkey/scan invalid key, got: %+v", f)
	}

	// Missing script → error.
	if f := findingFor(findings, "missing", "ghost"); f == nil || f.Level != "error" {
		t.Errorf("expected error finding for missing/ghost, got: %+v", f)
	}

	// Orphan script → warn (Tool "" on the finding).
	if f := findingFor(findings, "orphan", ""); f == nil || f.Level != "warn" {
		t.Errorf("expected orphan-script warning for orphan skill, got: %+v", f)
	}
	// The claimed script (real.sh) must NOT be flagged orphan.
	for _, f := range findings {
		if f.Skill == "orphan" && f.Tool == "" && filepath.Base(pathFromMsg(f.Msg)) == "real.sh" {
			t.Error("real.sh backs a tool and must not be an orphan finding")
		}
	}
}

// pathFromMsg extracts the "skills/.../x.sh" token from an orphan finding msg.
func pathFromMsg(msg string) string {
	for _, tok := range strings.Fields(msg) {
		if filepath.Ext(tok) == ".sh" {
			return tok
		}
	}
	return ""
}
