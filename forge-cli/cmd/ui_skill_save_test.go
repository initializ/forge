package cmd

import (
	"os"
	"path/filepath"
	"testing"

	forgeui "github.com/initializ/forge/forge-ui"
)

// TestSaveSkillToDisk_CreateNew_WritesSkillAndScripts pins the
// create-path behavior unchanged by issue #193 — a fresh skill lands
// at skills/<name>/SKILL.md with executable scripts beside it.
func TestSaveSkillToDisk_CreateNew_WritesSkillAndScripts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"),
		[]byte("agent_id: t\nversion: 0.1\nframework: forge\nmodel:\n  provider: openai\n  name: x\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	_, err := SaveSkillToDisk(forgeui.SkillSaveOptions{
		AgentDir:  root,
		SkillName: "fresh",
		SkillMD:   "---\nname: fresh\ndescription: x\n---\n# F\n## Tool: t\nA tool.\n",
		Scripts: map[string]string{
			"a.sh": "#!/bin/sh\necho a\n",
		},
	})
	if err != nil {
		t.Fatalf("SaveSkillToDisk: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "skills", "fresh", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "fresh", "scripts", "a.sh")); err != nil {
		t.Errorf("script a.sh not written: %v", err)
	}
}

// TestSaveSkillToDisk_Overwrite_DropsStaleScripts is the issue #193
// invariant: when the user edits a skill and removes one of its
// helper scripts, the stale .sh on disk MUST be gone after save.
// Otherwise the runtime (which globs the scripts/ directory) keeps
// finding and executing scripts the SKILL.md no longer references.
func TestSaveSkillToDisk_Overwrite_DropsStaleScripts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"),
		[]byte("agent_id: t\nversion: 0.1\nframework: forge\nmodel:\n  provider: openai\n  name: x\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Seed a skill with two scripts.
	_, err := SaveSkillToDisk(forgeui.SkillSaveOptions{
		AgentDir:  root,
		SkillName: "iter",
		SkillMD:   "---\nname: iter\ndescription: x\n---\n# I\n## Tool: t\nA tool.\n",
		Scripts: map[string]string{
			"old.sh":  "#!/bin/sh\necho old\n",
			"keep.sh": "#!/bin/sh\necho keep\n",
		},
	})
	if err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Edit: drop old.sh, modify keep.sh.
	_, err = SaveSkillToDisk(forgeui.SkillSaveOptions{
		AgentDir:    root,
		SkillName:   "iter",
		EditingName: "iter",
		Overwrite:   true,
		SkillMD:     "---\nname: iter\ndescription: x updated\n---\n# I\n## Tool: t\nA tool.\n",
		Scripts: map[string]string{
			"keep.sh": "#!/bin/sh\necho keep modified\n",
		},
	})
	if err != nil {
		t.Fatalf("overwrite save: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "skills", "iter", "scripts", "old.sh")); !os.IsNotExist(err) {
		t.Errorf("stale script old.sh still present after edit (err: %v)", err)
	}
	keep, err := os.ReadFile(filepath.Join(root, "skills", "iter", "scripts", "keep.sh"))
	if err != nil {
		t.Fatalf("keep.sh missing: %v", err)
	}
	if string(keep) != "#!/bin/sh\necho keep modified\n" {
		t.Errorf("keep.sh not updated; got %q", string(keep))
	}
}

// TestSaveSkillToDisk_Overwrite_NameMismatch_DoesNotWipeScripts is
// the defense-in-depth pin: even if a caller bypasses the handler
// and passes Overwrite=true with EditingName != SkillName, the
// scripts/-cleanup path must NOT fire. The handler is the primary
// guard; this is the belt to its suspenders.
func TestSaveSkillToDisk_Overwrite_NameMismatch_DoesNotWipeScripts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"),
		[]byte("agent_id: t\nversion: 0.1\nframework: forge\nmodel:\n  provider: openai\n  name: x\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	_, err := SaveSkillToDisk(forgeui.SkillSaveOptions{
		AgentDir:  root,
		SkillName: "victim",
		SkillMD:   "---\nname: victim\ndescription: x\n---\n# V\n## Tool: t\nA tool.\n",
		Scripts: map[string]string{
			"important.sh": "#!/bin/sh\necho important\n",
		},
	})
	if err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Caller bypasses the handler: overwrite=true with mismatched
	// editing_name. The RemoveAll branch must not fire because the
	// guard would have rejected this at the handler boundary.
	_, err = SaveSkillToDisk(forgeui.SkillSaveOptions{
		AgentDir:    root,
		SkillName:   "victim",
		EditingName: "attacker", // <-- mismatch
		Overwrite:   true,
		SkillMD:     "---\nname: victim\ndescription: y\n---\n# V\n## Tool: t\nA tool.\n",
		Scripts:     nil,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "victim", "scripts", "important.sh")); err != nil {
		t.Errorf("important.sh wiped under mismatched-name overwrite: %v", err)
	}
}
