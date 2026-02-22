package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

func TestDockerfileStage_Execute(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test-agent",
		Version: "0.1.0",
		Runtime: &agentspec.RuntimeConfig{
			Image:      "python:3.12-slim",
			Entrypoint: []string{"python", "agent.py"},
			Port:       8080,
		},
	}

	stage := &DockerfileStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "FROM python:3.12-slim") {
		t.Error("Dockerfile missing FROM line")
	}
	if !strings.Contains(content, `ENTRYPOINT ["python","agent.py"]`) {
		t.Errorf("Dockerfile missing/wrong ENTRYPOINT, got:\n%s", content)
	}
	if !strings.Contains(content, "EXPOSE 8080") {
		t.Error("Dockerfile missing EXPOSE")
	}

	if _, ok := bc.GeneratedFiles["Dockerfile"]; !ok {
		t.Error("Dockerfile not recorded in GeneratedFiles")
	}
}
