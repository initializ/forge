package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

func TestValidateStage_Valid(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})

	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "0.1.0",
		Name:         "test-agent",
		Runtime: &agentspec.RuntimeConfig{
			Image:      "python:3.12-slim",
			Entrypoint: []string{"python", "agent.py"},
			Port:       8080,
		},
	}
	bc.Spec = spec

	// Write agent.json
	data, _ := json.MarshalIndent(spec, "", "  ")
	os.WriteFile(filepath.Join(outDir, "agent.json"), data, 0644)

	// Write Dockerfile
	os.WriteFile(filepath.Join(outDir, "Dockerfile"), []byte("FROM python:3.12-slim\n"), 0644)

	stage := &ValidateStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestValidateStage_InvalidSpec(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{}

	// Write invalid agent.json (missing required fields)
	data := []byte(`{"forge_version": "1.0"}`)
	os.WriteFile(filepath.Join(outDir, "agent.json"), data, 0644)
	os.WriteFile(filepath.Join(outDir, "Dockerfile"), []byte("FROM ubuntu\n"), 0644)

	stage := &ValidateStage{}
	err := stage.Execute(context.Background(), bc)
	if err == nil {
		t.Fatal("expected error for invalid agent.json")
	}
}

func TestValidateStage_MissingDockerfile(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})

	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "0.1.0",
		Name:         "test-agent",
	}
	bc.Spec = spec

	data, _ := json.MarshalIndent(spec, "", "  ")
	os.WriteFile(filepath.Join(outDir, "agent.json"), data, 0644)
	// No Dockerfile

	stage := &ValidateStage{}
	err := stage.Execute(context.Background(), bc)
	if err == nil {
		t.Fatal("expected error for missing Dockerfile")
	}
}
