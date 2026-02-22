package crewai

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlugin_Name(t *testing.T) {
	p := &Plugin{}
	if p.Name() != "crewai" {
		t.Errorf("Name() = %q, want crewai", p.Name())
	}
}

func TestPlugin_DetectProject_RequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("crewai>=0.30\ncrewai-tools\n"), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("expected DetectProject to return true for requirements.txt with crewai")
	}
}

func TestPlugin_DetectProject_PyprojectToml(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[tool.poetry.dependencies]
crewai = "^0.30"
`), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("expected DetectProject to return true for pyproject.toml with crewai")
	}
}

func TestPlugin_DetectProject_PythonImport(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "agent.py"), []byte(`from crewai import Agent, Task, Crew
agent = Agent(role="researcher")
`), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("expected DetectProject to return true for .py with crewai import")
	}
}

func TestPlugin_DetectProject_NoMarkers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "agent.py"), []byte("print('hello')\n"), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if ok {
		t.Error("expected DetectProject to return false without crewai markers")
	}
}

func TestPlugin_DetectProject_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if ok {
		t.Error("expected DetectProject to return false for empty dir")
	}
}

func TestPlugin_ExtractAgentConfig_FullPattern(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "agent.py"), []byte(`
from crewai import Agent, Task, Crew
from crewai_tools import BaseTool

class WebSearchTool(BaseTool):
    name: str = "web_search"
    description: str = "Search the web for information"

    def _run(self, query: str) -> str:
        return "results"

agent = Agent(
    role="Research Analyst",
    goal="Find accurate information",
    backstory="Expert researcher with years of experience"
)
`), 0644)

	p := &Plugin{}
	cfg, err := p.ExtractAgentConfig(dir)
	if err != nil {
		t.Fatalf("ExtractAgentConfig error: %v", err)
	}

	if cfg.Identity == nil {
		t.Fatal("expected Identity to be set")
	}
	if cfg.Identity.Role != "Research Analyst" {
		t.Errorf("Role = %q, want 'Research Analyst'", cfg.Identity.Role)
	}
	if cfg.Identity.Goal != "Find accurate information" {
		t.Errorf("Goal = %q, want 'Find accurate information'", cfg.Identity.Goal)
	}
	if cfg.Identity.Backstory != "Expert researcher with years of experience" {
		t.Errorf("Backstory = %q", cfg.Identity.Backstory)
	}

	if len(cfg.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(cfg.Tools))
	}
	if cfg.Tools[0].Name != "web_search" {
		t.Errorf("Tool.Name = %q, want web_search", cfg.Tools[0].Name)
	}
	if cfg.Tools[0].Description != "Search the web for information" {
		t.Errorf("Tool.Description = %q", cfg.Tools[0].Description)
	}

	if cfg.Description != "Find accurate information" {
		t.Errorf("Description = %q, want 'Find accurate information'", cfg.Description)
	}
}

func TestPlugin_ExtractAgentConfig_NoPythonFiles(t *testing.T) {
	dir := t.TempDir()
	p := &Plugin{}
	cfg, err := p.ExtractAgentConfig(dir)
	if err != nil {
		t.Fatalf("ExtractAgentConfig error: %v", err)
	}
	if cfg.Identity != nil {
		t.Error("expected nil Identity for empty dir")
	}
	if len(cfg.Tools) != 0 {
		t.Error("expected no tools for empty dir")
	}
}

func TestPlugin_RuntimeDependencies(t *testing.T) {
	p := &Plugin{}
	deps := p.RuntimeDependencies()
	if len(deps) != 2 {
		t.Fatalf("expected 2 deps, got %d", len(deps))
	}
	if deps[0] != "crewai" || deps[1] != "crewai-tools" {
		t.Errorf("deps = %v", deps)
	}
}
