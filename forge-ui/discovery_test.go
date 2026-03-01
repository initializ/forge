package forgeui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFindsAgentsInSubdirs(t *testing.T) {
	root := t.TempDir()

	// Create agent-a
	agentA := filepath.Join(root, "agent-a")
	if err := os.MkdirAll(agentA, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentA, "forge.yaml"), `
agent_id: agent-a
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
tools:
  - name: web_search
  - name: http_request
`)

	// Create agent-b
	agentB := filepath.Join(root, "agent-b")
	if err := os.MkdirAll(agentB, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentB, "forge.yaml"), `
agent_id: agent-b
version: 0.2.0
framework: forge
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
`)

	scanner := NewScanner(root)
	agents, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}

	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	a := agents["agent-a"]
	if a == nil {
		t.Fatal("agent-a not found")
	}
	if a.Version != "0.1.0" {
		t.Errorf("agent-a version = %q, want %q", a.Version, "0.1.0")
	}
	if a.Model.Provider != "openai" {
		t.Errorf("agent-a provider = %q, want %q", a.Model.Provider, "openai")
	}
	if len(a.Tools) != 2 {
		t.Errorf("agent-a tools = %d, want 2", len(a.Tools))
	}
	if a.Status != StateStopped {
		t.Errorf("agent-a status = %q, want %q", a.Status, StateStopped)
	}

	b := agents["agent-b"]
	if b == nil {
		t.Fatal("agent-b not found")
	}
	if b.Model.Provider != "anthropic" {
		t.Errorf("agent-b provider = %q, want %q", b.Model.Provider, "anthropic")
	}
}

func TestScanSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()

	hidden := filepath.Join(root, ".hidden-agent")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(hidden, "forge.yaml"), `
agent_id: hidden
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)

	scanner := NewScanner(root)
	agents, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}

	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d (hidden dir should be skipped)", len(agents))
	}
}

func TestScanCountsSkills(t *testing.T) {
	root := t.TempDir()

	agentDir := filepath.Join(root, "my-agent")
	skillDir := filepath.Join(agentDir, "skills", "research")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `
agent_id: my-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# Research Skill")

	scanner := NewScanner(root)
	agents, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}

	agent := agents["my-agent"]
	if agent == nil {
		t.Fatal("my-agent not found")
	}
	if agent.Skills != 1 {
		t.Errorf("skills = %d, want 1", agent.Skills)
	}
}

func TestScanEmptyDir(t *testing.T) {
	root := t.TempDir()

	scanner := NewScanner(root)
	agents, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
