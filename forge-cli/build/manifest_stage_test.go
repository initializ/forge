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
