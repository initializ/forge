package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/compiler"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/plugins"
	"github.com/initializ/forge/forge-core/types"
)

func testConfig() *types.ForgeConfig {
	return &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Framework:  "langchain",
		Entrypoint: "python agent.py",
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4",
		},
		Tools: []types.ToolRef{
			{Name: "web-search", Type: "builtin"},
		},
	}
}

func TestConfigToAgentSpec(t *testing.T) {
	cfg := testConfig()
	spec := compiler.ConfigToAgentSpec(cfg)

	if spec.ForgeVersion != "1.0" {
		t.Errorf("ForgeVersion = %q, want %q", spec.ForgeVersion, "1.0")
	}
	if spec.AgentID != "test-agent" {
		t.Errorf("AgentID = %q, want %q", spec.AgentID, "test-agent")
	}
	if spec.Version != "0.1.0" {
		t.Errorf("Version = %q, want %q", spec.Version, "0.1.0")
	}
	if spec.Name != "test-agent" {
		t.Errorf("Name = %q, want %q", spec.Name, "test-agent")
	}
	if spec.Runtime == nil {
		t.Fatal("Runtime is nil")
	}
	if spec.Runtime.Port != 8080 {
		t.Errorf("Port = %d, want 8080", spec.Runtime.Port)
	}
	if len(spec.Runtime.Entrypoint) != 2 || spec.Runtime.Entrypoint[0] != "python" {
		t.Errorf("Entrypoint = %v, want [python agent.py]", spec.Runtime.Entrypoint)
	}
	if len(spec.Tools) != 1 || spec.Tools[0].Name != "web-search" {
		t.Errorf("Tools = %v, want [{web-search}]", spec.Tools)
	}
	if spec.Model == nil || spec.Model.Provider != "openai" {
		t.Error("Model not set correctly")
	}
}

func TestInferBaseImage(t *testing.T) {
	tests := []struct {
		ep   []string
		want string
	}{
		{[]string{"python", "agent.py"}, "python:3.12-slim"},
		{[]string{"python3", "agent.py"}, "python:3.12-slim"},
		{[]string{"bun", "run", "agent.ts"}, "oven/bun:latest"},
		{[]string{"go", "run", "."}, "golang:1.23-alpine"},
		{[]string{"node", "index.js"}, "node:20-slim"},
		{[]string{"java", "-jar", "app.jar"}, "ubuntu:latest"},
		{[]string{}, "ubuntu:latest"},
	}
	for _, tt := range tests {
		got := compiler.InferBaseImage(tt.ep)
		if got != tt.want {
			t.Errorf("InferBaseImage(%v) = %q, want %q", tt.ep, got, tt.want)
		}
	}
}

func TestAgentSpecStage_Execute(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Config = testConfig()

	stage := &AgentSpecStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if bc.Spec == nil {
		t.Fatal("Spec not set on BuildContext")
	}

	data, err := os.ReadFile(filepath.Join(outDir, "agent.json"))
	if err != nil {
		t.Fatalf("reading agent.json: %v", err)
	}

	var spec agentspec.AgentSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("unmarshalling agent.json: %v", err)
	}
	if spec.AgentID != "test-agent" {
		t.Errorf("agent.json AgentID = %q, want %q", spec.AgentID, "test-agent")
	}

	if _, ok := bc.GeneratedFiles["agent.json"]; !ok {
		t.Error("agent.json not recorded in GeneratedFiles")
	}
}

func TestMergePluginConfig_FillsGaps(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID: "test-agent",
		Name:    "test-agent", // same as AgentID, should be overwritten
		Tools: []agentspec.ToolSpec{
			{Name: "web-search"},
		},
	}

	pc := &plugins.AgentConfig{
		Name:        "Research Agent",
		Description: "An agent that researches topics",
		Tools: []plugins.ToolDefinition{
			{Name: "web-search", Description: "Search the web"}, // enrich existing
			{Name: "calculator", Description: "Do math"},        // new tool
		},
		Model: &plugins.PluginModelConfig{Provider: "openai", Name: "gpt-4"},
	}

	compiler.MergePluginConfig(spec, pc)

	if spec.Name != "Research Agent" {
		t.Errorf("Name = %q, want 'Research Agent'", spec.Name)
	}
	if spec.Description != "An agent that researches topics" {
		t.Errorf("Description = %q", spec.Description)
	}
	if len(spec.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(spec.Tools))
	}
	if spec.Tools[0].Description != "Search the web" {
		t.Errorf("Tool[0].Description = %q, want 'Search the web'", spec.Tools[0].Description)
	}
	if spec.Tools[1].Name != "calculator" {
		t.Errorf("Tool[1].Name = %q, want calculator", spec.Tools[1].Name)
	}
	if spec.Model == nil || spec.Model.Provider != "openai" {
		t.Error("Model not merged correctly")
	}
}

func TestMergePluginConfig_ForgeYAMLPrecedence(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID:     "my-agent",
		Name:        "My Custom Name", // different from AgentID, should NOT be overwritten
		Description: "Already set",
		Model: &agentspec.ModelConfig{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
		},
	}

	pc := &plugins.AgentConfig{
		Name:        "Plugin Name",
		Description: "Plugin description",
		Model:       &plugins.PluginModelConfig{Provider: "openai", Name: "gpt-4"},
	}

	compiler.MergePluginConfig(spec, pc)

	if spec.Name != "My Custom Name" {
		t.Errorf("Name should not be overwritten, got %q", spec.Name)
	}
	if spec.Description != "Already set" {
		t.Errorf("Description should not be overwritten, got %q", spec.Description)
	}
	if spec.Model.Provider != "anthropic" {
		t.Errorf("Model should not be overwritten, got %q", spec.Model.Provider)
	}
}

func TestWrapperEntrypoint(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"a2a_wrapper.py", []string{"python", "a2a_wrapper.py"}},
		{"wrapper.ts", []string{"bun", "run", "wrapper.ts"}},
		{"wrapper.go", []string{"go", "run", "wrapper.go"}},
		{"wrapper.unknown", []string{"python", "wrapper.unknown"}},
	}
	for _, tt := range tests {
		got := compiler.WrapperEntrypoint(tt.file)
		if len(got) != len(tt.want) {
			t.Errorf("WrapperEntrypoint(%q) = %v, want %v", tt.file, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("WrapperEntrypoint(%q)[%d] = %q, want %q", tt.file, i, got[i], tt.want[i])
			}
		}
	}
}

func TestAgentSpecStage_WithPluginConfig(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Entrypoint: "python agent.py",
	}
	bc.PluginConfig = &plugins.AgentConfig{
		Description: "Plugin-provided description",
		Tools: []plugins.ToolDefinition{
			{Name: "plugin-tool", Description: "From plugin"},
		},
	}
	bc.WrapperFile = "a2a_wrapper.py"

	stage := &AgentSpecStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if bc.Spec.Description != "Plugin-provided description" {
		t.Errorf("Description = %q", bc.Spec.Description)
	}
	if len(bc.Spec.Tools) != 1 || bc.Spec.Tools[0].Name != "plugin-tool" {
		t.Errorf("Tools = %v", bc.Spec.Tools)
	}
	if bc.Spec.Runtime.Entrypoint[0] != "python" || bc.Spec.Runtime.Entrypoint[1] != "a2a_wrapper.py" {
		t.Errorf("Entrypoint = %v, expected wrapper entrypoint", bc.Spec.Runtime.Entrypoint)
	}
}
