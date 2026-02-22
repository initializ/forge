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

func TestK8sStage_NetworkPolicy(t *testing.T) {
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

	// Check network-policy.yaml was generated
	npPath := filepath.Join(outDir, "k8s", "network-policy.yaml")
	data, err := os.ReadFile(npPath)
	if err != nil {
		t.Fatalf("reading network-policy.yaml: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "kind: NetworkPolicy") {
		t.Error("network-policy.yaml missing NetworkPolicy kind")
	}
	if !strings.Contains(content, "name: test-agent-network") {
		t.Error("network-policy.yaml missing agent name")
	}
	if !strings.Contains(content, "app: test-agent") {
		t.Error("network-policy.yaml missing app label")
	}

	// Default should be deny-all (no tools registered)
	if !strings.Contains(content, "egress: []") {
		t.Error("network-policy.yaml should have deny-all egress by default")
	}

	if _, ok := bc.GeneratedFiles["k8s/network-policy.yaml"]; !ok {
		t.Error("k8s/network-policy.yaml not recorded in GeneratedFiles")
	}
}

func TestK8sStage_NetworkPolicyAllowEgress(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "web-agent",
		Version: "0.2.0",
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

	npPath := filepath.Join(outDir, "k8s", "network-policy.yaml")
	data, err := os.ReadFile(npPath)
	if err != nil {
		t.Fatalf("reading network-policy.yaml: %v", err)
	}

	content := string(data)
	// Default is deny-all when no tools are set
	if !strings.Contains(content, "egress: []") {
		t.Error("expected deny-all egress for agent without network tools")
	}
}
