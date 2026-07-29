package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunBuild_FullPipeline(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: langchain
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
tools:
  - name: web-search
  - name: sql-query
`)

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	outDir := filepath.Join(dir, ".forge-output")
	oldOut := outputDir
	outputDir = outDir
	defer func() { outputDir = oldOut }()

	err := runBuild(nil, nil)
	if err != nil {
		t.Fatalf("runBuild() error: %v", err)
	}

	// Verify all expected output files
	expectedFiles := []string{
		"agent.json",
		"Dockerfile",
		"policy-scaffold.json",
		"build-manifest.json",
		"tools/web-search.schema.json",
		"tools/sql-query.schema.json",
		"k8s/deployment.yaml",
		"k8s/service.yaml",
	}

	for _, f := range expectedFiles {
		path := filepath.Join(outDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected output file missing: %s", f)
		}
	}

	// Verify agent.json content
	agentData, err := os.ReadFile(filepath.Join(outDir, "agent.json"))
	if err != nil {
		t.Fatalf("reading agent.json: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(agentData, &spec); err != nil {
		t.Fatalf("parsing agent.json: %v", err)
	}
	if spec["agent_id"] != "test-agent" {
		t.Errorf("agent_id = %v, want test-agent", spec["agent_id"])
	}

	// Verify build-manifest.json
	manifestData, err := os.ReadFile(filepath.Join(outDir, "build-manifest.json"))
	if err != nil {
		t.Fatalf("reading build-manifest.json: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("parsing build-manifest.json: %v", err)
	}
	files, ok := manifest["files"].([]any)
	if !ok {
		t.Fatal("manifest files is not an array")
	}
	if len(files) < 7 {
		t.Errorf("expected at least 7 files in manifest, got %d", len(files))
	}
}

func TestRunBuild_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: INVALID!
version: bad
entrypoint: ""
`)

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldOut := outputDir
	outputDir = filepath.Join(dir, ".forge-output")
	defer func() { outputDir = oldOut }()

	err := runBuild(nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

// TestParseLocalBins_NameValidation pins the --local-bin name check
// (PR #374 follow-up): the name flows verbatim into the .local-bins/
// path, the Dockerfile COPY, and the .dockerignore negation.
func TestParseLocalBins_NameValidation(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "somebin")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := parseLocalBins([]string{"forge=" + binPath}); err != nil {
		t.Errorf("valid name rejected: %v", err)
	}

	for _, name := range []string{"../escape", "..", ".", "a/b", "bad\nname", "*", "!x"} {
		if _, err := parseLocalBins([]string{name + "=" + binPath}); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}
