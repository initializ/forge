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

// TestEgressStage_LLMProviderBaseURL_InAllowlist is the issue #139
// end-to-end invariant: a forge.yaml with model.base_url declared
// produces an egress_allowlist.json whose all_domains carries the
// provider's hostname. Without this the generated Kubernetes
// NetworkPolicy blocks every LLM call to that host — the operator
// learns this only after deploy, when the agent times out or 401s.
func TestEgressStage_LLMProviderBaseURL_InAllowlist(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-together",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.github.com"}, // operator's explicit entry survives
		},
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "moonshotai/Kimi-K2.6",
			BaseURL:  "https://api.together.ai/v1",
			Fallbacks: []types.ModelFallback{
				{Provider: "anthropic", Name: "claude-sonnet-4", BaseURL: "https://anthropic-proxy.internal/v1"},
			},
		},
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test-together"}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "compiled", "egress_allowlist.json"))
	if err != nil {
		t.Fatalf("read egress_allowlist.json: %v", err)
	}
	var got struct {
		AllDomains []string `json:"all_domains"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode allowlist: %v", err)
	}

	// Both primary and fallback hosts must be present, plus the
	// operator's explicit entry. Order doesn't matter.
	wantHosts := []string{
		"api.github.com",           // operator-declared
		"api.together.ai",          // model.base_url
		"anthropic-proxy.internal", // fallback.base_url
	}
	for _, want := range wantHosts {
		if !contains(got.AllDomains, want) {
			t.Errorf("all_domains missing %q; got %v", want, got.AllDomains)
		}
	}
}

// TestEgressStage_NoBaseURL_AllowlistUnchanged confirms the
// backward-compat invariant: a forge.yaml without model.base_url
// produces an allowlist containing only what the operator declared
// + the existing AuthDomains/MCPDomains/OTelDomain auto-merges. No
// LLM provider hostname appears spuriously.
func TestEgressStage_NoBaseURL_AllowlistUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-no-baseurl",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.openai.com"},
		},
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4o",
			// no BaseURL
		},
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test-no-baseurl"}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(tmpDir, "compiled", "egress_allowlist.json"))
	var got struct {
		AllDomains []string `json:"all_domains"`
	}
	_ = json.Unmarshal(data, &got)

	// api.openai.com (operator-declared) must be present.
	if !contains(got.AllDomains, "api.openai.com") {
		t.Errorf("operator-declared api.openai.com missing; got %v", got.AllDomains)
	}
	// api.together.ai must NOT be there — nothing in the config referenced it.
	if contains(got.AllDomains, "api.together.ai") {
		t.Errorf("api.together.ai must not appear when no base_url is configured; got %v", got.AllDomains)
	}
}
