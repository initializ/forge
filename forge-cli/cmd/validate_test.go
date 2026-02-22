package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestForgeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "forge.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing forge.yaml: %v", err)
	}
	return path
}

func TestRunValidate_ValidConfig(t *testing.T) {
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
`)

	// Override the global cfgFile
	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldStrict := strict
	strict = false
	defer func() { strict = oldStrict }()

	err := runValidate(nil, nil)
	if err != nil {
		t.Fatalf("runValidate() error: %v", err)
	}
}

func TestRunValidate_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: INVALID_ID!
version: not-semver
entrypoint: ""
`)

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldStrict := strict
	strict = false
	defer func() { strict = oldStrict }()

	err := runValidate(nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestRunValidate_StrictMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: autogen
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
`)

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldStrict := strict
	strict = true
	defer func() { strict = oldStrict }()

	err := runValidate(nil, nil)
	if err == nil {
		t.Fatal("expected error in strict mode with unknown framework warning")
	}
}

func TestRunValidate_CommandCompat(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
`)

	// Create .forge-output with a valid agent.json
	outDir := filepath.Join(dir, ".forge-output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("creating .forge-output: %v", err)
	}
	agentJSON := `{
		"forge_version": "1.0",
		"agent_id": "test-agent",
		"version": "0.1.0",
		"name": "Test Agent",
		"runtime": {"image": "python:3.11-slim", "port": 8080},
		"model": {"provider": "openai", "name": "gpt-4"},
		"a2a": {"capabilities": {"streaming": true}}
	}`
	if err := os.WriteFile(filepath.Join(outDir, "agent.json"), []byte(agentJSON), 0644); err != nil {
		t.Fatalf("writing agent.json: %v", err)
	}

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldStrict := strict
	strict = false
	defer func() { strict = oldStrict }()

	oldCompat := commandCompat
	commandCompat = true
	defer func() { commandCompat = oldCompat }()

	err := runValidate(nil, nil)
	if err != nil {
		t.Fatalf("runValidate() with --command-compat error: %v", err)
	}
}

func TestRunValidate_CommandCompat_MissingRuntime(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
`)

	// agent.json without runtime
	outDir := filepath.Join(dir, ".forge-output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("creating .forge-output: %v", err)
	}
	agentJSON := `{
		"forge_version": "1.0",
		"agent_id": "test-agent",
		"version": "0.1.0",
		"name": "Test Agent"
	}`
	if err := os.WriteFile(filepath.Join(outDir, "agent.json"), []byte(agentJSON), 0644); err != nil {
		t.Fatalf("writing agent.json: %v", err)
	}

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldStrict := strict
	strict = false
	defer func() { strict = oldStrict }()

	oldCompat := commandCompat
	commandCompat = true
	defer func() { commandCompat = oldCompat }()

	err := runValidate(nil, nil)
	if err == nil {
		t.Fatal("expected error for missing runtime in command-compat mode")
	}
}

func TestRunValidate_CommandCompat_NoBuild(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
`)

	// No .forge-output directory

	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldStrict := strict
	strict = false
	defer func() { strict = oldStrict }()

	oldCompat := commandCompat
	commandCompat = true
	defer func() { commandCompat = oldCompat }()

	err := runValidate(nil, nil)
	if err == nil {
		t.Fatal("expected error when agent.json doesn't exist")
	}
}
