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

func TestK8sStage_Execute(t *testing.T) {
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

	stage := &K8sStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Check deployment.yaml
	depData, err := os.ReadFile(filepath.Join(outDir, "k8s", "deployment.yaml"))
	if err != nil {
		t.Fatalf("reading deployment.yaml: %v", err)
	}
	dep := string(depData)
	if !strings.Contains(dep, "name: test-agent") {
		t.Error("deployment.yaml missing agent name")
	}
	if !strings.Contains(dep, "image: python:3.12-slim") {
		t.Error("deployment.yaml missing image reference")
	}
	if !strings.Contains(dep, "containerPort: 8080") {
		t.Error("deployment.yaml missing container port")
	}

	// Check service.yaml
	svcData, err := os.ReadFile(filepath.Join(outDir, "k8s", "service.yaml"))
	if err != nil {
		t.Fatalf("reading service.yaml: %v", err)
	}
	svc := string(svcData)
	if !strings.Contains(svc, "name: test-agent") {
		t.Error("service.yaml missing agent name")
	}
	if !strings.Contains(svc, "targetPort: 8080") {
		t.Error("service.yaml missing target port")
	}

	if _, ok := bc.GeneratedFiles["k8s/deployment.yaml"]; !ok {
		t.Error("k8s/deployment.yaml not recorded in GeneratedFiles")
	}
	if _, ok := bc.GeneratedFiles["k8s/service.yaml"]; !ok {
		t.Error("k8s/service.yaml not recorded in GeneratedFiles")
	}
}
