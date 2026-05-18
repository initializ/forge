package build

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

func writeChannelYAML(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name+"-config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestChannelsStage_UnionsWithSkillEnvRequired(t *testing.T) {
	dir := t.TempDir()
	writeChannelYAML(t, dir, "slack", `
adapter: slack
settings:
  app_token_env: SLACK_APP_TOKEN
  bot_token_env: SLACK_BOT_TOKEN
`)
	writeChannelYAML(t, dir, "telegram", `
adapter: telegram
settings:
  bot_token_env: TELEGRAM_BOT_TOKEN
`)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: dir})
	bc.Config = &types.ForgeConfig{Channels: []string{"slack", "telegram"}}
	bc.Spec = &agentspec.AgentSpec{
		Requirements: &agentspec.AgentRequirements{
			EnvRequired: []string{"SKILL_API_KEY"},
		},
	}

	if err := (&ChannelsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("ChannelsStage.Execute: %v", err)
	}

	want := []string{"SKILL_API_KEY", "SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"}
	if !slices.Equal(bc.Spec.Requirements.EnvRequired, want) {
		t.Errorf("EnvRequired = %v, want %v", bc.Spec.Requirements.EnvRequired, want)
	}
}

func TestChannelsStage_PopulatesRequirementsWhenNil(t *testing.T) {
	// Project with channels but no skills — Spec.Requirements starts nil
	// because RequirementsStage early-returns. ChannelsStage must still
	// surface channel env vars to the manifests.
	dir := t.TempDir()
	writeChannelYAML(t, dir, "slack", `
adapter: slack
settings:
  bot_token_env: SLACK_BOT_TOKEN
`)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: dir})
	bc.Config = &types.ForgeConfig{Channels: []string{"slack"}}
	bc.Spec = &agentspec.AgentSpec{} // Requirements nil

	if err := (&ChannelsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bc.Spec.Requirements == nil {
		t.Fatal("Requirements should be created when channels declare env vars")
	}
	if !slices.Equal(bc.Spec.Requirements.EnvRequired, []string{"SLACK_BOT_TOKEN"}) {
		t.Errorf("EnvRequired = %v, want [SLACK_BOT_TOKEN]", bc.Spec.Requirements.EnvRequired)
	}
}

func TestChannelsStage_NoChannels(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: t.TempDir()})
	bc.Config = &types.ForgeConfig{}
	bc.Spec = &agentspec.AgentSpec{}

	if err := (&ChannelsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bc.Spec.Requirements != nil {
		t.Error("expected Requirements to stay nil for project with no channels")
	}
}

// TestChannelsStage_FlowsThroughToK8sManifests is the end-to-end regression
// for issue #50: channel env vars must appear in the generated K8s deployment
// and secret manifests, not only in docker-compose.
func TestChannelsStage_FlowsThroughToK8sManifests(t *testing.T) {
	workDir := t.TempDir()
	outDir := t.TempDir()
	writeChannelYAML(t, workDir, "slack", `
adapter: slack
settings:
  app_token_env: SLACK_APP_TOKEN
  bot_token_env: SLACK_BOT_TOKEN
`)
	writeChannelYAML(t, workDir, "telegram", `
adapter: telegram
settings:
  bot_token_env: TELEGRAM_BOT_TOKEN
`)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   workDir,
		OutputDir: outDir,
	})
	bc.Config = &types.ForgeConfig{Channels: []string{"slack", "telegram"}}
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test-agent",
		Version: "0.1.0",
		Runtime: &agentspec.RuntimeConfig{
			Image:      "python:3.12-slim",
			Entrypoint: []string{"python", "agent.py"},
			Port:       8080,
		},
	}

	if err := (&ChannelsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("ChannelsStage.Execute: %v", err)
	}
	if err := (&K8sStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("K8sStage.Execute: %v", err)
	}

	dep := readFile(t, filepath.Join(outDir, "k8s", "deployment.yaml"))
	sec := readFile(t, filepath.Join(outDir, "k8s", "secrets.yaml"))

	for _, want := range []string{"SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"} {
		if !strings.Contains(dep, want) {
			t.Errorf("deployment.yaml missing channel env var %q", want)
		}
		if !strings.Contains(sec, want) {
			t.Errorf("secrets.yaml missing channel env var %q", want)
		}
	}
	if !strings.Contains(dep, "secretKeyRef:") {
		t.Error("deployment.yaml should reference channel env vars via secretKeyRef")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestChannelsStage_MissingConfigWarns(t *testing.T) {
	// channels: [slack] declared, but no slack-config.yaml on disk.
	// The stage must surface a warning and not fail the build.
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: t.TempDir()})
	bc.Config = &types.ForgeConfig{Channels: []string{"slack"}}
	bc.Spec = &agentspec.AgentSpec{}

	if err := (&ChannelsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bc.Warnings) != 1 || !strings.Contains(bc.Warnings[0], "slack") {
		t.Errorf("expected one warning mentioning slack, got %v", bc.Warnings)
	}
	if bc.Spec.Requirements != nil {
		t.Error("Requirements should stay nil when no channel env vars discovered")
	}
}
