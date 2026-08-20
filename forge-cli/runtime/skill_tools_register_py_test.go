package runtime

import (
	"os"
	"os/exec"
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
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; interpreter-availability guard would skip registration")
	}
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

// TestRegisterSkillTools_SkipsMissingInterpreter checks that a .py tool is NOT
// registered when its interpreter isn't on PATH (it would fail at call time),
// while a .sh tool in the same skill still registers (bash is exempt).
func TestRegisterSkillTools_SkipsMissingInterpreter(t *testing.T) {
	wd := t.TempDir()
	skillDir := filepath.Join(wd, "skills", "mix")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: mix\ndescription: mixed\n---\n" +
		"## Tool: py_one\n**Input:** a (string)\n**Output:** o\n" +
		"## Tool: sh_two\n**Input:** a (string)\n**Output:** o\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"py-one.py", "sh-two.sh"} {
		if err := os.WriteFile(filepath.Join(skillDir, "scripts", f), []byte("x\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Empty PATH → LookPath("python3") fails; bash is exempt from the check.
	t.Setenv("PATH", "")

	r := &Runner{cfg: RunnerConfig{WorkDir: wd, Config: &types.ForgeConfig{}}, logger: nopLogger{}}
	reg := tools.NewRegistry()
	r.registerSkillTools(reg, "", "")

	if reg.Get("py_one") != nil {
		t.Error("py_one should be skipped when python3 is unavailable")
	}
	if reg.Get("sh_two") == nil {
		t.Error("sh_two (bash) should still register regardless of PATH")
	}
}
