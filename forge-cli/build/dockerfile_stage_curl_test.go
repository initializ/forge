package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/packaging"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

// renderForgeDockerfile runs the DockerfileStage for a forge-framework agent
// whose skills declare the given runtime binaries, and returns the Dockerfile.
func renderForgeDockerfile(t *testing.T, bins ...string) string {
	t.Helper()
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Config = &types.ForgeConfig{AgentID: "test", Version: "0.1.0", Framework: "forge"}
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test",
		Version: "0.1.0",
		Runtime: &agentspec.RuntimeConfig{
			Image:      "debian:bookworm-slim",
			Entrypoint: []string{"forge", "serve"},
			Port:       8080,
		},
	}
	reqs := make([]contract.BinRequirement, 0, len(bins))
	for _, b := range bins {
		reqs = append(reqs, contract.BinRequirement{Name: b})
	}
	bc.BinManifest = &packaging.BinManifest{Requirements: reqs, SkillOrigin: map[string]string{}}

	if err := (&DockerfileStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	return string(data)
}

// TestDockerfile_ForgeBootstrapKeepsRequiredCurl pins the fix for the
// forge-framework bootstrap clobbering a skill's declared curl. The
// bootstrap borrows curl to fetch the forge binary and used to
// `apt-get purge -y curl` afterwards — which removed the curl a skill
// (e.g. weather) needs at runtime, leaving CLI Exec 0/1. When curl is a
// declared runtime bin the purge must be skipped.
func TestDockerfile_ForgeBootstrapKeepsRequiredCurl(t *testing.T) {
	df := renderForgeDockerfile(t, "curl")

	// Sanity: the forge bootstrap RUN is present (framework=forge).
	if !strings.Contains(df, "forge-Linux-") {
		t.Fatalf("expected forge bootstrap RUN, got:\n%s", df)
	}
	// curl must be installed as a runtime apt package...
	if !strings.Contains(df, "curl") {
		t.Fatalf("expected curl in Dockerfile, got:\n%s", df)
	}
	// ...and must NOT be purged, or the skill loses it at runtime.
	if strings.Contains(df, "apt-get purge -y curl") {
		t.Errorf("forge bootstrap purges curl even though it is a declared runtime bin:\n%s", df)
	}
}

// TestDockerfile_ForgeBootstrapPurgesUnneededCurl is the complement: when
// no skill needs curl, the bootstrap should still purge it (curl was only
// borrowed to download the forge binary), keeping the image slim.
func TestDockerfile_ForgeBootstrapPurgesUnneededCurl(t *testing.T) {
	df := renderForgeDockerfile(t, "jq")

	if !strings.Contains(df, "forge-Linux-") {
		t.Fatalf("expected forge bootstrap RUN, got:\n%s", df)
	}
	if !strings.Contains(df, "apt-get purge -y curl") {
		t.Errorf("bootstrap should purge the borrowed curl when no skill needs it:\n%s", df)
	}
}
