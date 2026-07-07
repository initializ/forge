package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates skills/<dir>/SKILL.md under root with the given
// frontmatter name + body, and returns root.
func writeSkill(t *testing.T, root, dir, frontmatterName, body string) {
	t.Helper()
	skillDir := filepath.Join(root, "skills", dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + frontmatterName + "\ndescription: demo\n---\n" + body
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSkill(t *testing.T, root, name string) string {
	t.Helper()
	out, err := NewReadSkillTool(root).Execute(context.Background(),
		json.RawMessage(`{"name":`+mustJSON(name)+`}`))
	if err != nil {
		t.Fatalf("Execute(%q): %v", name, err)
	}
	return out
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

// TestReadSkill_ResolvesByFrontmatterNameWhenDirDiffers is the core
// regression for the skill-lookup bug: the loadable name advertised to
// the LLM (the frontmatter name) must resolve even when the skill
// directory is named differently.
func TestReadSkill_ResolvesByFrontmatterNameWhenDirDiffers(t *testing.T) {
	root := t.TempDir()
	// Directory "kube" but frontmatter name "k8s-incident-triage".
	writeSkill(t, root, "kube", "k8s-incident-triage", "# Triage\nRead-only kubectl triage.\n")

	// The name the catalog advertises (frontmatter name) must resolve.
	if out := readSkill(t, root, "k8s-incident-triage"); strings.Contains(out, "not found") {
		t.Errorf("frontmatter name did not resolve: %s", out)
	}
	// The directory name must still resolve (back-compat).
	if out := readSkill(t, root, "kube"); strings.Contains(out, "not found") {
		t.Errorf("directory name did not resolve: %s", out)
	}
}

// TestReadSkill_NormalizedMatch — case and underscore/hyphen drift from
// the model must not break resolution.
func TestReadSkill_NormalizedMatch(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "k8s-incident-triage", "k8s-incident-triage", "# body\n")
	for _, variant := range []string{"k8s_incident_triage", "K8S-Incident-Triage", "k8s-incident-triage"} {
		if out := readSkill(t, root, variant); strings.Contains(out, "not found") {
			t.Errorf("variant %q did not resolve: %s", variant, out)
		}
	}
}

// TestReadSkill_FlatFormat — skills/<name>.md layout still resolves.
func TestReadSkill_FlatFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "weather.md"), []byte("---\nname: weather\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := readSkill(t, root, "weather"); strings.Contains(out, "not found") {
		t.Errorf("flat skill did not resolve: %s", out)
	}
}

// TestReadSkill_NotFoundListsAvailable — a miss returns the loadable
// names so the model can retry instead of giving up.
func TestReadSkill_NotFoundListsAvailable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "kube", "k8s-incident-triage", "# body\n")
	out := readSkill(t, root, "nonexistent")
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found, got %s", out)
	}
	if !strings.Contains(out, "available_skills") || !strings.Contains(out, "k8s-incident-triage") {
		t.Errorf("not-found should list available skills incl. the frontmatter name: %s", out)
	}
}

// TestReadSkill_ListsSkillFiles — loading a skill surfaces its helper
// scripts (any language) and reference material so the model knows they
// exist and can read/run them.
func TestReadSkill_ListsSkillFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "k8s-incident-triage", "k8s-incident-triage", "# body\n")
	base := filepath.Join(root, "skills", "k8s-incident-triage")
	for _, f := range []struct{ rel, body string }{
		{"scripts/triage.py", "print('x')"},
		{"scripts/collect.sh", "echo x"},
		{"scripts/render.js", "console.log(1)"},
		{"reference/runbook.md", "# runbook"},
		{"reference/slo.yaml", "target: 0.99"},
	} {
		p := filepath.Join(base, f.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := readSkill(t, root, "k8s-incident-triage")
	for _, want := range []string{
		"## Skill files",
		"skills/k8s-incident-triage/scripts/triage.py — python",
		"skills/k8s-incident-triage/scripts/collect.sh — shell",
		"skills/k8s-incident-triage/scripts/render.js — javascript",
		"skills/k8s-incident-triage/reference/runbook.md",
		"skills/k8s-incident-triage/reference/slo.yaml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("skill files listing missing %q\nfull:\n%s", want, out)
		}
	}
	// SKILL.md itself must not be listed.
	if strings.Contains(out, "skills/k8s-incident-triage/SKILL.md") {
		t.Errorf("SKILL.md should not be listed among skill files:\n%s", out)
	}
}

// TestReadSkill_FlatFormatNoFileListing — a flat skills/<name>.md skill
// has no companion directory, so no file listing is appended.
func TestReadSkill_FlatFormatNoFileListing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "weather.md"), []byte("---\nname: weather\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := readSkill(t, root, "weather"); strings.Contains(out, "## Skill files") {
		t.Errorf("flat skill should have no file listing: %s", out)
	}
}

// TestReadSkill_TraversalRejected keeps the directory-traversal guard.
func TestReadSkill_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../etc/passwd", "a/b", `a\b`, ".."} {
		out := readSkill(t, root, bad)
		if !strings.Contains(out, "invalid skill name") {
			t.Errorf("traversal %q not rejected: %s", bad, out)
		}
	}
}
