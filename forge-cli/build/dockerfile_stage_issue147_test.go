package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

// TestDockerignore_ExcludesOperatorSideArtifacts pins the fix for
// issue #147 — the build's .dockerignore must keep operator-side
// artifacts (k8s manifests, build metadata, security audit report,
// the Dockerfile itself) out of the runtime container image. Pre-fix
// `COPY . .` blindly dumped all of them into /app/.
func TestDockerignore_ExcludesOperatorSideArtifacts(t *testing.T) {
	_ = context.Background()
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.Config = &types.ForgeConfig{AgentID: "test", Version: "1.0.0"}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	s := &DockerfileStage{}
	if err := s.writeDockerignore(bc); err != nil {
		t.Fatalf("writeDockerignore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, ".dockerignore"))
	if err != nil {
		t.Fatalf("reading .dockerignore: %v", err)
	}
	got := string(data)

	mustExclude := []string{
		// Secrets — preserved from the legacy list.
		".env", ".env.*", "*.enc", "secrets.enc", "*.key", "*.pem",
		// Issue #147 additions.
		"k8s/",
		"build-manifest.json",
		"compiled/",
		"Dockerfile",
		".dockerignore",
		".local-bins/",
	}
	for _, p := range mustExclude {
		if !strings.Contains(got, p+"\n") {
			t.Errorf(".dockerignore is missing required exclusion %q. Full content:\n%s", p, got)
		}
	}
}

// TestDockerignore_KeepsRuntimeFiles is the under-exclusion guard.
// The runtime opens forge.yaml, guardrails.json, <channel>-config.yaml,
// agent.json, policy-scaffold.json, skills/, checksums.json — none of
// those may appear as an exclusion pattern or the container would
// fail to start.
func TestDockerignore_KeepsRuntimeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.Config = &types.ForgeConfig{AgentID: "test", Version: "1.0.0"}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	s := &DockerfileStage{}
	if err := s.writeDockerignore(bc); err != nil {
		t.Fatalf("writeDockerignore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, ".dockerignore"))
	if err != nil {
		t.Fatalf("reading .dockerignore: %v", err)
	}
	got := string(data)

	mustNotExclude := []string{
		"forge.yaml",
		"guardrails.json",
		"agent.json",
		"policy-scaffold.json",
		"checksums.json",
		"slack-config.yaml",
		"telegram-config.yaml",
		"msteams-config.yaml",
		"skills/",
	}
	for _, p := range mustNotExclude {
		for _, line := range strings.Split(got, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || trimmed == "" {
				continue
			}
			if trimmed == p {
				t.Errorf(".dockerignore must NOT exclude runtime-required file %q. Full content:\n%s", p, got)
			}
		}
	}
}

// TestDockerfile_NoLongerCopiesDeadCompiledArtifacts confirms the
// Dockerfile template no longer carries the redundant lines that
// copied compiled/prompt.txt and compiled/skills/skills.json into
// /app — those files are no longer generated (issue #147) so the
// COPYs would also fail on a real build.
func TestDockerfile_NoLongerCopiesDeadCompiledArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Framework:  "forge",
		Entrypoint: "agent",
	}
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test",
		Version: "1.0.0",
		Runtime: &agentspec.RuntimeConfig{Image: "debian:bookworm-slim", Entrypoint: []string{"agent"}, Port: 8080},
	}
	bc.SkillsCount = 2

	s := &DockerfileStage{}
	if err := s.generateTemplateDockerfile(bc); err != nil {
		t.Fatalf("generateTemplateDockerfile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	got := string(data)

	mustNotContain := []string{
		"COPY compiled/skills/",
		"COPY compiled/prompt.txt",
	}
	for _, p := range mustNotContain {
		if strings.Contains(got, p) {
			t.Errorf("Dockerfile must not contain dead COPY %q (issue #147). Full content:\n%s", p, got)
		}
	}
}
