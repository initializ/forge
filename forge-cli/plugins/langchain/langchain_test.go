package langchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlugin_Name(t *testing.T) {
	p := &Plugin{}
	if p.Name() != "langchain" {
		t.Errorf("Name() = %q, want langchain", p.Name())
	}
}

func TestPlugin_DetectProject_RequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("langchain>=0.1\nlangchain-openai\n"), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("expected DetectProject to return true")
	}
}

func TestPlugin_DetectProject_PyprojectToml(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[tool.poetry.dependencies]
langchain = "^0.1"
`), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("expected DetectProject to return true")
	}
}

func TestPlugin_DetectProject_PythonImport(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "agent.py"), []byte(`from langchain.agents import AgentExecutor
from langchain_openai import ChatOpenAI
`), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("expected DetectProject to return true")
	}
}

func TestPlugin_DetectProject_NoMarkers(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "agent.py"), []byte("print('hello')\n"), 0644)

	p := &Plugin{}
	ok, err := p.DetectProject(dir)
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if ok {
		t.Error("expected DetectProject to return false")
	}
}

func TestPlugin_ExtractAgentConfig_ToolDecorators(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "tools.py"), []byte(`from langchain.tools import tool

@tool
def web_search(query: str) -> str:
    """Search the web for information"""
    return "results"

@tool
def calculator(expression: str) -> str:
    """Calculate mathematical expressions"""
    return str(eval(expression))
`), 0644)

	p := &Plugin{}
	cfg, err := p.ExtractAgentConfig(dir)
	if err != nil {
		t.Fatalf("ExtractAgentConfig error: %v", err)
	}

	if len(cfg.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(cfg.Tools))
	}
	if cfg.Tools[0].Name != "web_search" {
		t.Errorf("Tool[0].Name = %q, want web_search", cfg.Tools[0].Name)
	}
	if cfg.Tools[1].Name != "calculator" {
		t.Errorf("Tool[1].Name = %q, want calculator", cfg.Tools[1].Name)
	}
}

func TestPlugin_ExtractAgentConfig_ModelExtraction(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "agent.py"), []byte(`from langchain_openai import ChatOpenAI
llm = ChatOpenAI(model="gpt-4-turbo")
`), 0644)

	p := &Plugin{}
	cfg, err := p.ExtractAgentConfig(dir)
	if err != nil {
		t.Fatalf("ExtractAgentConfig error: %v", err)
	}

	if cfg.Model == nil {
		t.Fatal("expected Model to be set")
	}
	if cfg.Model.Name != "gpt-4-turbo" {
		t.Errorf("Model.Name = %q, want gpt-4-turbo", cfg.Model.Name)
	}
}

func TestPlugin_ExtractAgentConfig_NoPythonFiles(t *testing.T) {
	dir := t.TempDir()
	p := &Plugin{}
	cfg, err := p.ExtractAgentConfig(dir)
	if err != nil {
		t.Fatalf("ExtractAgentConfig error: %v", err)
	}
	if len(cfg.Tools) != 0 {
		t.Error("expected no tools for empty dir")
	}
}

func TestPlugin_RuntimeDependencies(t *testing.T) {
	p := &Plugin{}
	deps := p.RuntimeDependencies()
	if len(deps) != 3 {
		t.Fatalf("expected 3 deps, got %d", len(deps))
	}
	if deps[0] != "langchain" {
		t.Errorf("deps[0] = %q, want langchain", deps[0])
	}
}
