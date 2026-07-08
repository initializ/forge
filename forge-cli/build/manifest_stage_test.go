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

func TestManifestStage_Execute(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test-agent",
		Version: "0.1.0",
	}
	bc.AddFile("agent.json", filepath.Join(outDir, "agent.json"))
	bc.AddFile("Dockerfile", filepath.Join(outDir, "Dockerfile"))

	stage := &ManifestStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "build-manifest.json"))
	if err != nil {
		t.Fatalf("reading build-manifest.json: %v", err)
	}

	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshalling manifest: %v", err)
	}

	if manifest["agent_id"] != "test-agent" {
		t.Errorf("agent_id = %v, want test-agent", manifest["agent_id"])
	}
	if manifest["version"] != "0.1.0" {
		t.Errorf("version = %v, want 0.1.0", manifest["version"])
	}
	if manifest["built_at"] == nil {
		t.Error("built_at is nil")
	}

	files, ok := manifest["files"].([]any)
	if !ok {
		t.Fatalf("files is not an array: %T", manifest["files"])
	}
	// Should include agent.json, Dockerfile, and build-manifest.json itself
	if len(files) < 2 {
		t.Errorf("expected at least 2 files, got %d", len(files))
	}
}

func TestManifestStage_EnvRequirements(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test-agent",
		Version: "0.1.0",
		Requirements: &agentspec.AgentRequirements{
			EnvRequired: []string{"OPENAI_API_KEY", "SLACK_BOT_TOKEN"},
			EnvOptional: []string{"LOG_LEVEL"},
		},
	}

	stage := &ManifestStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "build-manifest.json"))
	if err != nil {
		t.Fatalf("reading build-manifest.json: %v", err)
	}

	var manifest struct {
		EnvRequired []string `json:"env_required"`
		EnvOptional []string `json:"env_optional"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshalling manifest: %v", err)
	}

	if len(manifest.EnvRequired) != 2 || manifest.EnvRequired[0] != "OPENAI_API_KEY" || manifest.EnvRequired[1] != "SLACK_BOT_TOKEN" {
		t.Errorf("env_required = %v, want [OPENAI_API_KEY SLACK_BOT_TOKEN]", manifest.EnvRequired)
	}
	if len(manifest.EnvOptional) != 1 || manifest.EnvOptional[0] != "LOG_LEVEL" {
		t.Errorf("env_optional = %v, want [LOG_LEVEL]", manifest.EnvOptional)
	}
}
