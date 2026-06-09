package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

// TestEgressStage_OTelEndpointInAllowlist is the Phase 6 (#107) end-to-end
// invariant: when forge.yaml has observability.tracing enabled with an
// endpoint, the build pipeline's egress_allowlist.json carries the
// collector hostname so the generated NetworkPolicy will admit OTLP
// traffic. Without this, a deploy ships with tracing enabled but
// silently fails to export — spans accumulate in the queue and drop
// on shutdown timeout, leaving the operator with an empty trace
// backend.
func TestEgressStage_OTelEndpointInAllowlist(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.example.com"},
		},
		Observability: types.ObservabilityConfig{
			Tracing: types.TracingYAML{
				Enabled:  true,
				Endpoint: "https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces",
			},
		},
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Read the generated allowlist file and confirm the collector
	// hostname is present. We assert on the FILE rather than on the
	// in-memory bc.EgressResolved because the file is what the
	// k8s_stage downstream + the runtime egress matcher consume —
	// regressing only the file would be invisible to a struct-only
	// assertion.
	data, err := os.ReadFile(filepath.Join(tmpDir, "compiled", "egress_allowlist.json"))
	if err != nil {
		t.Fatalf("read egress_allowlist.json: %v", err)
	}
	var got struct {
		AllowedDomains []string `json:"allowed_domains"`
		AllDomains     []string `json:"all_domains"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode allowlist: %v", err)
	}

	const want = "otel-collector.monitoring.svc.cluster.local"
	if !contains(got.AllDomains, want) {
		t.Errorf("all_domains missing collector host %q; got %v", want, got.AllDomains)
	}
	// The operator's manually-declared domain must still be present
	// — Phase 6 adds the collector, it does not replace anything.
	if !contains(got.AllDomains, "api.example.com") {
		t.Errorf("explicit allowed_domains entry lost; got all_domains=%v", got.AllDomains)
	}
}

// TestEgressStage_OTelDisabled_NoExtraEntry — the corollary backward-
// compat check. A forge.yaml with no observability.tracing block (or
// Enabled=false) must NOT inject anything new into the allowlist. The
// pre-Phase-6 set of entries is preserved verbatim.
func TestEgressStage_OTelDisabled_NoExtraEntry(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.example.com"},
		},
		Observability: types.ObservabilityConfig{
			Tracing: types.TracingYAML{
				Enabled:  false,
				Endpoint: "https://otel.example.com:4318/v1/traces",
			},
		},
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(tmpDir, "compiled", "egress_allowlist.json"))
	var got struct {
		AllDomains []string `json:"all_domains"`
	}
	_ = json.Unmarshal(data, &got)
	if contains(got.AllDomains, "otel.example.com") {
		t.Errorf("disabled tracing leaked collector host into allowlist; got %v", got.AllDomains)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
