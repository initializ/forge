package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plain SKILL.md (no metadata.forge) with a .py script that reads env + calls a URL.
const inferSkillMD = `---
name: infer-me
description: a plain skill
---
## Tool: t
**Input:** x (string)
`

func TestInferForgeMeta(t *testing.T) {
	src := t.TempDir()
	writeImportFile(t, filepath.Join(src, "SKILL.md"), inferSkillMD)
	writeImportFile(t, filepath.Join(src, "scripts", "fetch.py"),
		"import os, urllib.request\n"+
			"key = os.environ[\"DATADOG_API_KEY\"]\n"+
			"tok = os.getenv(\"DD_APP_KEY\")\n"+
			"urllib.request.urlopen(\"https://api.datadoghq.com/api/v1/query\")\n")
	writeImportFile(t, filepath.Join(src, "scripts", "render.js"),
		"const u = process.env.RENDER_URL;\n")

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: src, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}

	if res.SuggestedForgeMeta == "" {
		t.Fatal("expected a suggested metadata.forge block for a plain skill")
	}
	s := res.SuggestedForgeMeta
	// interpreters inferred with confidence
	for _, want := range []string{"python3", "node"} {
		if !strings.Contains(s, "- "+want) {
			t.Errorf("suggested block missing bin %q:\n%s", want, s)
		}
	}
	// env + egress are commented candidates (review), not active declarations
	for _, want := range []string{"DATADOG_API_KEY", "DD_APP_KEY", "RENDER_URL", "api.datadoghq.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("suggested block missing candidate %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "# egress_domains") || !strings.Contains(s, "# env:") {
		t.Errorf("egress/env should be COMMENTED candidates, not active:\n%s", s)
	}
}

func TestShellEnvReads(t *testing.T) {
	script := `#!/usr/bin/env bash
set -euo pipefail
: "${DATADOG_API_KEY:?}"          # required env → candidate
TOKEN="$DD_APP_KEY"               # read → candidate
LOCAL_TMP=$(mktemp)               # assigned locally → NOT a candidate
echo "$LOCAL_TMP" > "$HOME/out"   # HOME is a common shell var → excluded
for ITEM in a b c; do echo "$ITEM"; done   # ITEM assigned by for → excluded
curl -H "apikey: $DATADOG_API_KEY" https://api.example.com
echo "$1 $PATH"                   # positional + PATH → excluded
`
	got := map[string]bool{}
	for _, n := range shellEnvReads(script) {
		got[n] = true
	}
	for _, want := range []string{"DATADOG_API_KEY", "DD_APP_KEY"} {
		if !got[want] {
			t.Errorf("expected %q in shell env candidates, got %v", want, got)
		}
	}
	for _, notWant := range []string{"LOCAL_TMP", "HOME", "ITEM", "PATH"} {
		if got[notWant] {
			t.Errorf("%q should be filtered out of shell env candidates", notWant)
		}
	}
}

// TestInferForgeMeta_ShellScript exercises the full inference path for a
// shell-backed skill.
func TestInferForgeMeta_ShellScript(t *testing.T) {
	src := t.TempDir()
	writeImportFile(t, filepath.Join(src, "SKILL.md"), inferSkillMD)
	writeImportFile(t, filepath.Join(src, "scripts", "run.sh"),
		"#!/usr/bin/env bash\n: \"${WEATHER_API_KEY:?}\"\ncurl \"https://api.weather.example/v1?key=$WEATHER_API_KEY\"\n")

	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: src, AgentDir: newAgentDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	s := res.SuggestedForgeMeta
	// .sh needs no requires.bins (bash is built in) but env/egress candidates surface.
	if !strings.Contains(s, "WEATHER_API_KEY") {
		t.Errorf("shell env candidate missing:\n%s", s)
	}
	if !strings.Contains(s, "api.weather.example") {
		t.Errorf("shell egress candidate missing:\n%s", s)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "f.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "hello" {
		t.Fatalf("content = %q err=%v, want hello", got, err)
	}
	// Overwrite atomically; no leftover temp files in the dir.
	if err := atomicWriteFile(path, []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "world" {
		t.Errorf("overwrite content = %q, want world", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestImportSkillFolder_WriteForgeMeta(t *testing.T) {
	src := t.TempDir()
	writeImportFile(t, filepath.Join(src, "SKILL.md"), inferSkillMD)
	writeImportFile(t, filepath.Join(src, "scripts", "run.py"), "print(1)\n")

	agentDir := newAgentDir(t)
	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: src, AgentDir: agentDir, WriteForgeMeta: true})
	if err != nil {
		t.Fatal(err)
	}
	// The block is written, so nothing left to print.
	if res.SuggestedForgeMeta != "" {
		t.Error("SuggestedForgeMeta should be cleared after a successful write")
	}
	// The stale "does not list python3" warning must be gone.
	for _, w := range res.Warnings {
		if strings.Contains(w, "does not list python3") {
			t.Errorf("stale python3 warning survived after injection: %q", w)
		}
	}
	// requires.bins landed in the vendored SKILL.md and it still parses.
	md, _ := os.ReadFile(filepath.Join(agentDir, "skills", "infer-me", "SKILL.md"))
	if !strings.Contains(string(md), "requires:") || !strings.Contains(string(md), "- python3") {
		t.Errorf("requires.bins not injected:\n%s", md)
	}
	if !hasForgeMeta(string(md)) {
		t.Errorf("injected frontmatter should parse as metadata.forge:\n%s", md)
	}
	// The body must be intact (delimiter handling didn't eat content).
	if !strings.Contains(string(md), "## Tool: t") {
		t.Errorf("body lost after injection:\n%s", md)
	}
}

func TestImportSkillFolder_WriteForgeMeta_SkipsExistingForgeMeta(t *testing.T) {
	src := t.TempDir()
	withForge := "---\nname: has-forge\ndescription: x\n" +
		"metadata:\n  forge:\n    requires:\n      bins: [curl]\n---\n## Tool: t\n**Input:** x (string)\n"
	writeImportFile(t, filepath.Join(src, "SKILL.md"), withForge)
	writeImportFile(t, filepath.Join(src, "scripts", "run.py"), "print(1)\n")

	res, err := ImportSkillFolder(SkillImportOptions{SourceDir: src, AgentDir: newAgentDir(t), WriteForgeMeta: true})
	if err != nil {
		t.Fatal(err)
	}
	// Author already declared forge meta → no suggestion, no injection.
	if res.SuggestedForgeMeta != "" {
		t.Errorf("should not suggest a block when forge meta exists: %q", res.SuggestedForgeMeta)
	}
}

func TestInjectForgeMetaBins_SkipsExistingMetadataKey(t *testing.T) {
	// A metadata: block without forge — injecting a second metadata: would be
	// invalid YAML, so injection must skip with a message.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "x")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: x\ndescription: y\nmetadata:\n  other:\n    k: v\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, msg := injectForgeMetaBins(skillDir, inferredForgeMeta{Bins: []string{"python3"}})
	if wrote {
		t.Error("must not inject a second metadata: key")
	}
	if !strings.Contains(msg, "already has a metadata:") {
		t.Errorf("expected a skip message about existing metadata, got %q", msg)
	}
}
