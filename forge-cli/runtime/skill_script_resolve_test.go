package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func writeScript(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestResolveSkillScript_Interpreters is the #405 D2 core: a `## Tool:` entry
// resolves to a .sh/.py/.js script with the matching interpreter.
func TestResolveSkillScript_Interpreters(t *testing.T) {
	wd := t.TempDir()
	r := &Runner{cfg: RunnerConfig{WorkDir: wd}, logger: nopLogger{}}

	cases := []struct {
		file        string // relative to skills/mine/scripts
		tool        string
		wantInterp  string
		wantRelPath string
	}{
		{"do-thing.sh", "do_thing", "bash", "skills/mine/scripts/do-thing.sh"},
		{"fetch-data.py", "fetch_data", "python3", "skills/mine/scripts/fetch-data.py"},
		{"render.js", "render", "node", "skills/mine/scripts/render.js"},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			writeScript(t, filepath.Join(wd, "skills", "mine", "scripts", c.file))
			rel, interp, found := r.resolveSkillScript("mine", c.tool)
			if !found {
				t.Fatalf("resolveSkillScript(%q) not found", c.tool)
			}
			if interp != c.wantInterp {
				t.Errorf("interpreter = %q, want %q", interp, c.wantInterp)
			}
			if filepath.ToSlash(rel) != c.wantRelPath {
				t.Errorf("relPath = %q, want %q", rel, c.wantRelPath)
			}
			// skillEntryHasScript must agree (shared resolver).
			if !r.skillEntryHasScript("mine", c.tool) {
				t.Errorf("skillEntryHasScript disagrees for %q", c.tool)
			}
		})
	}
}

// TestResolveSkillScript_ShellPreferredWithinDir: when both .sh and .py exist
// for the same tool in the same dir, shell wins (back-compat).
func TestResolveSkillScript_ShellPreferredWithinDir(t *testing.T) {
	wd := t.TempDir()
	r := &Runner{cfg: RunnerConfig{WorkDir: wd}, logger: nopLogger{}}
	writeScript(t, filepath.Join(wd, "skills", "s", "scripts", "t.sh"))
	writeScript(t, filepath.Join(wd, "skills", "s", "scripts", "t.py"))
	_, interp, found := r.resolveSkillScript("s", "t")
	if !found || interp != "bash" {
		t.Errorf("expected .sh preferred (bash), got interp=%q found=%v", interp, found)
	}
}

// TestResolveSkillScript_SkillLocalBeforeShared: a skill-local script wins over
// a shared skills/scripts/ one.
func TestResolveSkillScript_SkillLocalBeforeShared(t *testing.T) {
	wd := t.TempDir()
	r := &Runner{cfg: RunnerConfig{WorkDir: wd}, logger: nopLogger{}}
	writeScript(t, filepath.Join(wd, "skills", "scripts", "t.sh"))         // shared
	writeScript(t, filepath.Join(wd, "skills", "mine", "scripts", "t.py")) // skill-local
	rel, interp, found := r.resolveSkillScript("mine", "t")
	if !found {
		t.Fatal("not found")
	}
	if interp != "python3" || filepath.ToSlash(rel) != "skills/mine/scripts/t.py" {
		t.Errorf("skill-local should win; got rel=%q interp=%q", rel, interp)
	}
}

func TestResolveSkillScript_NotFound(t *testing.T) {
	wd := t.TempDir()
	r := &Runner{cfg: RunnerConfig{WorkDir: wd}, logger: nopLogger{}}
	if _, _, found := r.resolveSkillScript("mine", "nope"); found {
		t.Error("expected not found for a missing script")
	}
	if r.skillEntryHasScript("mine", "nope") {
		t.Error("skillEntryHasScript should be false for a missing script")
	}
}

// TestResolveSkillScript_RejectsTraversalName is the D2 security regression: a
// `## Tool:` name with path traversal must not resolve to an out-of-tree
// script, even when a matching file exists there.
func TestResolveSkillScript_RejectsTraversalName(t *testing.T) {
	wd := t.TempDir()
	r := &Runner{cfg: RunnerConfig{WorkDir: wd}, logger: nopLogger{}}
	// Plant a script an escaping name would otherwise resolve to.
	writeScript(t, filepath.Join(wd, "evil.sh"))
	writeScript(t, filepath.Join(wd, "skills", "other", "scripts", "steal.sh"))

	for _, name := range []string{
		"../../evil",             // climbs out of skills/<dir>/scripts
		"../../../evil",          // further
		"../other/scripts/steal", // hop into another skill's scripts
		"/etc/passwd",            // absolute
	} {
		if rel, _, found := r.resolveSkillScript("mine", name); found {
			t.Errorf("traversal tool name %q resolved to %q — must be rejected", name, rel)
		}
		if r.skillEntryHasScript("mine", name) {
			t.Errorf("skillEntryHasScript accepted traversal name %q", name)
		}
	}
}
