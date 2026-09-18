package surface

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildScaffoldArgv(t *testing.T) {
	raw := json.RawMessage(`{"name":"rss-digest","model_provider":"anthropic","tools":["web_fetch"],"channels":["slack"],"force":true}`)
	args, dir, err := buildScaffoldArgv(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"init", "rss-digest", "--non-interactive", "--model-provider", "anthropic", "--tools", "web_fetch", "--channels", "slack", "--force"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("argv = %v, want %v", args, want)
	}
	if dir != "" {
		t.Errorf("scaffold should run in base, got dir %q", dir)
	}
}

func TestBuildScaffoldArgvValidation(t *testing.T) {
	if _, _, err := buildScaffoldArgv(json.RawMessage(`{"model_provider":"openai"}`)); err == nil {
		t.Error("missing name should error")
	}
	if _, _, err := buildScaffoldArgv(json.RawMessage(`{"name":"x"}`)); err == nil {
		t.Error("missing model_provider should error")
	}
}

func TestChannelAndSkillArgv(t *testing.T) {
	args, dir, err := buildChannelArgv(json.RawMessage(`{"channel":"slack","dir":"rss-digest"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"channel", "add", "slack"}) || dir != "rss-digest" {
		t.Errorf("channel argv=%v dir=%q", args, dir)
	}
	sa, sdir, err := buildSkillArgv(json.RawMessage(`{"name":"weather"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sa, []string{"skills", "add", "weather"}) || sdir != "" {
		t.Errorf("skill argv=%v dir=%q", sa, sdir)
	}
}

func TestDirOnly(t *testing.T) {
	b := dirOnly("forge_validate", "validate")
	args, dir, err := b(json.RawMessage(`{"dir":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"validate"}) || dir != "agent" {
		t.Errorf("argv=%v dir=%q", args, dir)
	}
	// nil/empty args are allowed (no dir).
	args2, dir2, err := b(nil)
	if err != nil || dir2 != "" || !reflect.DeepEqual(args2, []string{"validate"}) {
		t.Errorf("nil args: argv=%v dir=%q err=%v", args2, dir2, err)
	}
}

func TestSafeJoin(t *testing.T) {
	base := filepath.FromSlash("/work")
	if got := safeJoin(base, ""); got != base {
		t.Errorf("empty dir: %q", got)
	}
	if got := safeJoin(base, "sub"); got != filepath.Join(base, "sub") {
		t.Errorf("sub: %q", got)
	}
	// Escapes are refused → fall back to base.
	if got := safeJoin(base, "../etc"); got != base {
		t.Errorf("traversal not blocked: %q", got)
	}
	if got := safeJoin(base, filepath.FromSlash("/abs")); got != base {
		t.Errorf("absolute not blocked: %q", got)
	}
}

func TestForgeOpsToolsShape(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range ForgeOpsTools("/work") {
		names[tool.Name()] = true
		if tool.Description() == "" {
			t.Errorf("%s has no description", tool.Name())
		}
		var js map[string]any
		if err := json.Unmarshal(tool.InputSchema(), &js); err != nil {
			t.Errorf("%s has invalid input schema: %v", tool.Name(), err)
		}
	}
	for _, want := range []string{"forge_scaffold", "forge_validate", "forge_build", "forge_run", "forge_add_channel", "forge_add_skill"} {
		if !names[want] {
			t.Errorf("missing forge-ops tool %q", want)
		}
	}
}

func TestScaffoldFromSkillDir(t *testing.T) {
	args, _, err := buildScaffoldArgv(json.RawMessage(`{"name":"a","model_provider":"anthropic","from_skill_dir":"./anthropic-skill"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--from-skill-dir ./anthropic-skill") {
		t.Errorf("expected --from-skill-dir in argv: %v", args)
	}
}

func TestImportSkillArgv(t *testing.T) {
	args, dir, err := buildImportSkillArgv(json.RawMessage(`{"folder":"./sk","write_forge_meta":true,"dir":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "skills import ./sk --write-forge-meta" || dir != "agent" {
		t.Errorf("import argv=%v dir=%q", args, dir)
	}
	if _, _, err := buildImportSkillArgv(json.RawMessage(`{}`)); err == nil {
		t.Error("missing folder should error")
	}
}

func TestBuildForgeCLIArgv(t *testing.T) {
	args, dir, err := buildForgeCLIArgv(json.RawMessage(`{"args":["skills","list"],"dir":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "skills list" || dir != "agent" {
		t.Errorf("argv=%v dir=%q", args, dir)
	}
	if _, _, err := buildForgeCLIArgv(json.RawMessage(`{"args":[]}`)); err == nil {
		t.Error("empty args should error")
	}
	for _, cmd := range []string{"run", "serve", "ui"} {
		if _, _, err := buildForgeCLIArgv(json.RawMessage(`{"args":["` + cmd + `"]}`)); err == nil {
			t.Errorf("%q should be refused", cmd)
		}
	}
}
