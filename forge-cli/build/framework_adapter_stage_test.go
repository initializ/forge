package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-cli/plugins/crewai"
	"github.com/initializ/forge/forge-cli/plugins/custom"
	"github.com/initializ/forge/forge-cli/plugins/langchain"
)

func newTestRegistry() *plugins.FrameworkRegistry {
	reg := plugins.NewFrameworkRegistry()
	reg.Register(&crewai.Plugin{})
	reg.Register(&langchain.Plugin{})
	reg.Register(&custom.Plugin{})
	return reg
}

func TestFrameworkAdapterStage_ExplicitFramework(t *testing.T) {
	workDir := t.TempDir()
	outDir := t.TempDir()

	// Create a crewai project
	os.WriteFile(filepath.Join(workDir, "agent.py"), []byte(`
from crewai import Agent
agent = Agent(role="Tester", goal="Test things")
`), 0644)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   workDir,
		OutputDir: outDir,
	})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Framework:  "crewai",
		Entrypoint: "python agent.py",
	}

	stage := &FrameworkAdapterStage{Registry: newTestRegistry()}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if bc.PluginConfig == nil {
		t.Fatal("expected PluginConfig to be set")
	}

	// CrewAI should generate a wrapper
	if bc.WrapperFile == "" {
		t.Error("expected WrapperFile to be set for crewai")
	}

	// Wrapper file should exist on disk
	if _, err := os.Stat(filepath.Join(outDir, bc.WrapperFile)); os.IsNotExist(err) {
		t.Error("wrapper file not found on disk")
	}
}

func TestFrameworkAdapterStage_AutoDetect(t *testing.T) {
	workDir := t.TempDir()
	outDir := t.TempDir()

	// Create langchain markers
	os.WriteFile(filepath.Join(workDir, "requirements.txt"), []byte("langchain\n"), 0644)
	os.WriteFile(filepath.Join(workDir, "agent.py"), []byte(`
from langchain.tools import tool
@tool
def search(query: str) -> str:
    """Search the web"""
    return "result"
`), 0644)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   workDir,
		OutputDir: outDir,
	})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Framework:  "", // auto-detect
		Entrypoint: "python agent.py",
	}

	stage := &FrameworkAdapterStage{Registry: newTestRegistry()}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if bc.PluginConfig == nil {
		t.Fatal("expected PluginConfig to be set via auto-detect")
	}
	if bc.WrapperFile == "" {
		t.Error("expected WrapperFile from langchain auto-detect")
	}
}

func TestFrameworkAdapterStage_CustomNoWrapper(t *testing.T) {
	workDir := t.TempDir()
	outDir := t.TempDir()

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   workDir,
		OutputDir: outDir,
	})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Framework:  "custom",
		Entrypoint: "python agent.py",
	}

	stage := &FrameworkAdapterStage{Registry: newTestRegistry()}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if bc.PluginConfig == nil {
		t.Fatal("expected PluginConfig to be set")
	}

	// Custom should NOT generate a wrapper
	if bc.WrapperFile != "" {
		t.Errorf("expected empty WrapperFile for custom, got %q", bc.WrapperFile)
	}
}

func TestFrameworkAdapterStage_NilRegistry(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Entrypoint: "python agent.py",
	}

	stage := &FrameworkAdapterStage{Registry: nil}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if bc.PluginConfig != nil {
		t.Error("expected nil PluginConfig with nil registry")
	}
}

func TestFrameworkAdapterStage_VerboseDeps(t *testing.T) {
	workDir := t.TempDir()
	outDir := t.TempDir()

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   workDir,
		OutputDir: outDir,
	})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Framework:  "crewai",
		Entrypoint: "python agent.py",
	}
	bc.Verbose = true

	stage := &FrameworkAdapterStage{Registry: newTestRegistry()}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Should have warnings for runtime deps
	if len(bc.Warnings) == 0 {
		t.Error("expected warnings for runtime dependencies in verbose mode")
	}
}
