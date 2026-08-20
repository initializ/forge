package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/types"
)

// TestRegisterSkillTools_PythonToolIsFirstClass is the #405 D2 integration
// check: a `## Tool:` entry backed by a .py script registers as a first-class
// callable tool (previously .sh-only), and skillEntryHasScript agrees so it's
// excluded from the read_skill catalog.
func TestRegisterSkillTools_PythonToolIsFirstClass(t *testing.T) {
	wd := t.TempDir()
	skillDir := filepath.Join(wd, "skills", "py-tools")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: py-tools\ndescription: Python tools\n" +
		"metadata:\n  forge:\n    requires:\n      bins:\n        - python3\n---\n" +
		"## Tool: greet_user\nGreet someone.\n**Input:** name (string)\n**Output:** greeting\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "greet-user.py"), []byte("print(1)\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{cfg: RunnerConfig{WorkDir: wd, Config: &types.ForgeConfig{}}, logger: nopLogger{}}
	reg := tools.NewRegistry()
	r.registerSkillTools(reg, "", "")

	if reg.Get("greet_user") == nil {
		t.Fatalf("greet_user (.py-backed) not registered as a first-class tool; registry: %v", reg.List())
	}
	// It's a first-class tool, so the catalog must exclude it.
	if !r.skillEntryHasScript("py-tools", "greet_user") {
		t.Error("skillEntryHasScript should report the .py tool as script-backed")
	}
}
