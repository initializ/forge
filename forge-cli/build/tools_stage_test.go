package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

func TestToolsStage_NoTools(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{Tools: nil}

	stage := &ToolsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// tools/ directory should not exist
	if _, err := os.Stat(filepath.Join(outDir, "tools")); !os.IsNotExist(err) {
		t.Error("tools/ directory should not be created when no tools")
	}
}

func TestToolsStage_WithTools(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{
		Tools: []agentspec.ToolSpec{
			{Name: "web-search"},
			{Name: "sql-query"},
		},
	}

	stage := &ToolsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	for _, name := range []string{"web-search.schema.json", "sql-query.schema.json"} {
		path := filepath.Join(outDir, "tools", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", path)
		}
	}

	if len(bc.GeneratedFiles) != 2 {
		t.Errorf("expected 2 generated files, got %d", len(bc.GeneratedFiles))
	}
}
