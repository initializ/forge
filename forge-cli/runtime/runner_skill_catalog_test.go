package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
)

// writeCatalogSkill creates skills/<dir>/SKILL.md with a frontmatter name
// plus a `## Tool:` heading whose name differs from the skill name — the
// exact shape that produced the read_skill lookup mismatch.
func writeCatalogSkill(t *testing.T, root, dir, name, toolHeading string) {
	t.Helper()
	d := filepath.Join(root, "skills", dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: Read-only kubectl triage.\n---\n" +
		"## Tool: " + toolHeading + "\n\nDoes the triage.\n"
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillCatalog_AdvertisesLoadableName asserts the catalog lists the
// skill by its loadable (frontmatter) name, not the internal `## Tool:`
// heading — the fix for the skill-lookup bug.
func TestSkillCatalog_AdvertisesLoadableName(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "k8s-incident-triage", "k8s-incident-triage", "k8s_triage")

	r := &Runner{cfg: RunnerConfig{WorkDir: root, Config: &types.ForgeConfig{AgentID: "test"}}}
	cat := r.buildSkillCatalog()

	// The read_skill key (leading identifier) must be the loadable name.
	if !strings.Contains(cat, "- k8s-incident-triage:") {
		t.Errorf("catalog should key the line on the loadable name; got:\n%s", cat)
	}
	// The tool heading must NOT be advertised as a loadable skill (the bug).
	if strings.Contains(cat, "- k8s_triage:") {
		t.Errorf("catalog must not key a line on the internal tool name; got:\n%s", cat)
	}
	// But the tool name IS surfaced as a capability for tool selection.
	if !strings.Contains(cat, "provides: k8s_triage") {
		t.Errorf("catalog should surface the skill's tool names under provides; got:\n%s", cat)
	}
}

// TestSkillCatalog_NameIsReadSkillResolvable is the contract test that
// would have caught the original bug: every name the catalog advertises
// to the LLM must resolve through read_skill against the same workDir.
func TestSkillCatalog_NameIsReadSkillResolvable(t *testing.T) {
	root := t.TempDir()
	// Two skills, both with a `## Tool:` heading that differs from the
	// skill name, and one whose directory differs from its name.
	writeCatalogSkill(t, root, "k8s-incident-triage", "k8s-incident-triage", "k8s_triage")
	writeCatalogSkill(t, root, "kube", "cluster-inspector", "inspect")

	r := &Runner{cfg: RunnerConfig{WorkDir: root, Config: &types.ForgeConfig{AgentID: "test"}}}
	cat := r.buildSkillCatalog()

	readTool := builtins.NewReadSkillTool(root)
	var checked int
	for _, line := range strings.Split(cat, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		// Extract the advertised name: "- <name>: <desc>".
		name := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, "- "), ":", 2)[0])
		if name == "" {
			continue
		}
		out, err := readTool.Execute(context.Background(),
			json.RawMessage(`{"name":`+mustQuote(name)+`}`))
		if err != nil {
			t.Fatalf("read_skill(%q): %v", name, err)
		}
		if strings.Contains(out, "not found") {
			t.Errorf("catalog advertised %q but read_skill could not resolve it: %s", name, out)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("expected 2 advertised skills, checked %d\ncatalog:\n%s", checked, cat)
	}
}

func mustQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
