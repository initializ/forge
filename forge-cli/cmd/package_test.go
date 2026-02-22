package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func TestComputeImageTag(t *testing.T) {
	tests := []struct {
		name     string
		agentID  string
		version  string
		registry string
		want     string
	}{
		{
			name:    "without registry",
			agentID: "my-agent",
			version: "1.0.0",
			want:    "my-agent:1.0.0",
		},
		{
			name:     "with registry",
			agentID:  "my-agent",
			version:  "1.0.0",
			registry: "ghcr.io/org",
			want:     "ghcr.io/org/my-agent:1.0.0",
		},
		{
			name:     "with docker hub registry",
			agentID:  "my-agent",
			version:  "0.2.0",
			registry: "docker.io/myuser",
			want:     "docker.io/myuser/my-agent:0.2.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeImageTag(tt.agentID, tt.version, tt.registry)
			if got != tt.want {
				t.Errorf("computeImageTag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureBuildOutput_MissingManifest(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: langchain
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
tools:
  - name: web-search
`)

	outDir := filepath.Join(dir, ".forge-output")

	// Set globals for runBuild
	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldOut := outputDir
	outputDir = outDir
	defer func() { outputDir = oldOut }()

	// ensureBuildOutput should trigger a build since manifest is missing
	err := ensureBuildOutput(outDir, cfgPath)
	if err != nil {
		t.Fatalf("ensureBuildOutput() error: %v", err)
	}

	// build-manifest.json should now exist
	if _, err := os.Stat(filepath.Join(outDir, "build-manifest.json")); os.IsNotExist(err) {
		t.Error("build-manifest.json not created after ensureBuildOutput")
	}
}

func TestEnsureBuildOutput_FreshManifest(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: langchain
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
tools:
  - name: web-search
`)

	outDir := filepath.Join(dir, ".forge-output")

	// Set globals for runBuild
	oldCfg := cfgFile
	cfgFile = cfgPath
	defer func() { cfgFile = oldCfg }()

	oldOut := outputDir
	outputDir = outDir
	defer func() { outputDir = oldOut }()

	// First call should build
	if err := ensureBuildOutput(outDir, cfgPath); err != nil {
		t.Fatalf("first ensureBuildOutput() error: %v", err)
	}

	// Second call should skip build since manifest is fresh
	if err := ensureBuildOutput(outDir, cfgPath); err != nil {
		t.Fatalf("second ensureBuildOutput() error: %v", err)
	}
}

func TestWithChannelsFlagDefault(t *testing.T) {
	if withChannels {
		t.Error("--with-channels should default to false")
	}
}

func TestGenerateDockerCompose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yaml")

	cfg := &types.ForgeConfig{
		AgentID:    "my-agent",
		Version:    "0.1.0",
		Entrypoint: "python main.py",
		Channels:   []string{"a2a", "slack", "telegram"},
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4",
		},
		Egress: types.EgressRef{
			Profile: "standard",
			Mode:    "allowlist",
		},
	}

	err := generateDockerCompose(path, "my-agent:0.1.0", cfg, 8080)
	if err != nil {
		t.Fatalf("generateDockerCompose() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading docker-compose.yaml: %v", err)
	}

	content := string(data)

	// Check agent service
	if !strings.Contains(content, "image: my-agent:0.1.0") {
		t.Error("missing agent image")
	}
	if !strings.Contains(content, "\"8080:8080\"") {
		t.Error("missing agent port mapping")
	}

	// Check model env vars
	if !strings.Contains(content, "FORGE_PROVIDER=openai") {
		t.Error("missing FORGE_PROVIDER env var")
	}
	if !strings.Contains(content, "FORGE_MODEL=gpt-4") {
		t.Error("missing FORGE_MODEL env var")
	}
	if !strings.Contains(content, "FORGE_API_KEY=${FORGE_API_KEY}") {
		t.Error("missing FORGE_API_KEY env var")
	}

	// Check egress labels
	if !strings.Contains(content, "forge.egress.profile: standard") {
		t.Error("missing egress profile label")
	}
	if !strings.Contains(content, "forge.egress.mode: allowlist") {
		t.Error("missing egress mode label")
	}

	// Check slack adapter
	if !strings.Contains(content, "slack-adapter:") {
		t.Error("missing slack-adapter service")
	}
	if !strings.Contains(content, "SLACK_SIGNING_SECRET") {
		t.Error("missing SLACK_SIGNING_SECRET env var")
	}
	if !strings.Contains(content, "SLACK_BOT_TOKEN") {
		t.Error("missing SLACK_BOT_TOKEN env var")
	}

	// Check telegram adapter
	if !strings.Contains(content, "telegram-adapter:") {
		t.Error("missing telegram-adapter service")
	}
	if !strings.Contains(content, "TELEGRAM_BOT_TOKEN") {
		t.Error("missing TELEGRAM_BOT_TOKEN env var")
	}

	// Non-adapter channels (a2a) should be skipped
	if strings.Contains(content, "a2a-adapter") {
		t.Error("a2a should not generate an adapter service")
	}

	// All adapters should depend on agent
	if !strings.Contains(content, "depends_on:") {
		t.Error("adapters should depend on agent")
	}
	if !strings.Contains(content, "AGENT_URL=http://agent:8080") {
		t.Error("adapters should reference agent URL")
	}
}

func TestGenerateDockerCompose_NoAdapters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yaml")

	cfg := &types.ForgeConfig{
		AgentID:    "my-agent",
		Version:    "0.1.0",
		Entrypoint: "python main.py",
		Channels:   []string{"a2a", "http"},
	}

	err := generateDockerCompose(path, "my-agent:0.1.0", cfg, 8080)
	if err != nil {
		t.Fatalf("generateDockerCompose() error: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// Only agent service, no adapters
	if strings.Contains(content, "-adapter:") {
		t.Error("should not generate adapter services for non-adapter channels")
	}
}
