package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillScript lays out skills/<skill>/<rel> under root.
func writeSkillScript(t *testing.T, root, skill, rel, body string) {
	t.Helper()
	p := filepath.Join(root, "skills", skill, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
}

func runScript(t *testing.T, root, skill, path string, args map[string]any) string {
	t.Helper()
	argsJSON, _ := json.Marshal(args)
	in, _ := json.Marshal(map[string]any{"skill": skill, "path": path, "args": json.RawMessage(argsJSON)})
	out, err := NewRunSkillScriptTool(root, "", nil).Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute(%s): %v", path, err)
	}
	return out
}

// TestRunSkillScript_Languages runs a shell, python, and javascript helper
// script that (a) echoes back the JSON args it received on $1 and (b) reads
// a sibling file by a relative path — proving both the args contract and
// that CWD is the skill directory.
func TestRunSkillScript_Languages(t *testing.T) {
	root := t.TempDir()
	const skill = "owl"
	writeSkillScript(t, root, skill, "SKILL.md", "---\nname: owl\ndescription: d\n---\n# Owl\n")
	writeSkillScript(t, root, skill, "reference/data.txt", "HELLO")

	// Each script prints JSON: {"got": <args>, "ref": "<reference/data.txt>"}.
	writeSkillScript(t, root, skill, "scripts/check.sh",
		"#!/usr/bin/env bash\nset -euo pipefail\nINPUT=\"${1:-}\"\nprintf '{\"got\": %s, \"ref\": \"%s\"}\\n' \"$INPUT\" \"$(cat reference/data.txt)\"\n")
	writeSkillScript(t, root, skill, "scripts/check.py",
		"import sys, json\na = json.loads(sys.argv[1])\nref = open('reference/data.txt').read().strip()\nprint(json.dumps({'got': a, 'ref': ref}))\n")
	writeSkillScript(t, root, skill, "scripts/check.js",
		"const a = JSON.parse(process.argv[2]);\nconst fs = require('fs');\nconst ref = fs.readFileSync('reference/data.txt','utf8').trim();\nconsole.log(JSON.stringify({got: a, ref}));\n")

	cases := []struct {
		name, path, interp string
	}{
		{"shell", "scripts/check.sh", "bash"},
		{"python", "scripts/check.py", "python3"},
		{"javascript", "scripts/check.js", "node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.interp); err != nil {
				t.Skipf("%s not installed", tc.interp)
			}
			out := runScript(t, root, skill, tc.path, map[string]any{"n": 42})
			var got struct {
				Got map[string]any `json:"got"`
				Ref string         `json:"ref"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
				t.Fatalf("output not the expected JSON: %v\nraw: %s", err, out)
			}
			if got.Ref != "HELLO" {
				t.Errorf("relative file read failed (CWD not skill dir?): ref=%q", got.Ref)
			}
			if got.Got["n"] != float64(42) {
				t.Errorf("args not passed as $1: got=%v", got.Got)
			}
		})
	}
}

// TestRunSkillScript_Traversal rejects paths escaping the skill directory.
func TestRunSkillScript_Traversal(t *testing.T) {
	root := t.TempDir()
	writeSkillScript(t, root, "owl", "SKILL.md", "---\nname: owl\ndescription: d\n---\n")
	// A secret outside the skill dir the traversal must not reach.
	if err := os.WriteFile(filepath.Join(root, "secret.sh"), []byte("echo LEAK\n"), 0o755); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}
	for _, bad := range []string{"../../secret.sh", "../secret.sh", "/etc/hostname"} {
		out := runScript(t, root, "owl", bad, nil)
		if !strings.Contains(out, "error") || strings.Contains(out, "LEAK") {
			t.Errorf("traversal %q not rejected: %s", bad, out)
		}
	}
}

// TestRunSkillScript_UnsupportedExt returns a clear error for non-runnable
// extensions (e.g. .ts).
func TestRunSkillScript_UnsupportedExt(t *testing.T) {
	root := t.TempDir()
	writeSkillScript(t, root, "owl", "SKILL.md", "---\nname: owl\ndescription: d\n---\n")
	writeSkillScript(t, root, "owl", "scripts/x.ts", "console.log(1)\n")
	out := runScript(t, root, "owl", "scripts/x.ts", nil)
	if !strings.Contains(out, "unsupported script type") {
		t.Errorf("expected unsupported-type error, got: %s", out)
	}
}

// TestRunSkillScript_UnknownSkill returns not-found for an unknown skill.
func TestRunSkillScript_UnknownSkill(t *testing.T) {
	root := t.TempDir()
	out := runScript(t, root, "ghost", "scripts/x.sh", nil)
	if !strings.Contains(out, "not found") {
		t.Errorf("expected skill-not-found, got: %s", out)
	}
}

func TestInterpreterForScript(t *testing.T) {
	ok := map[string]string{"a.sh": "bash", "a.bash": "bash", "a.py": "python3", "a.js": "node", "a.mjs": "node"}
	for path, want := range ok {
		got, err := interpreterForScript(path)
		if err != nil || got != want {
			t.Errorf("interpreterForScript(%q) = %q,%v want %q", path, got, err, want)
		}
	}
	if _, err := interpreterForScript("a.rb"); err == nil {
		t.Error("expected error for .rb")
	}
}
