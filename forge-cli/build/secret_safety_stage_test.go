package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

func TestSecretSafetyStage_ProdEncryptedFileOnly(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: t.TempDir()})
	bc.Config = &types.ForgeConfig{
		Secrets: types.SecretsConfig{
			Providers: []string{"encrypted-file"},
		},
	}
	bc.ProdMode = true

	stage := &SecretSafetyStage{}
	err := stage.Execute(context.Background(), bc)
	if err == nil {
		t.Fatal("expected error for prod mode with encrypted-file only")
	}
	if !strings.Contains(err.Error(), "container-compatible") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretSafetyStage_ProdWithEnv(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: t.TempDir()})
	bc.Config = &types.ForgeConfig{
		Secrets: types.SecretsConfig{
			Providers: []string{"encrypted-file", "env"},
		},
	}
	bc.ProdMode = true

	stage := &SecretSafetyStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretSafetyStage_EncryptedFileOnlyWarning(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: t.TempDir()})
	bc.Config = &types.ForgeConfig{
		Secrets: types.SecretsConfig{
			Providers: []string{"encrypted-file"},
		},
	}

	stage := &SecretSafetyStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bc.Warnings) == 0 {
		t.Fatal("expected warning for encrypted-file-only config")
	}
}

func TestSecretSafetyStage_MissingDockerignore(t *testing.T) {
	dir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: dir})
	bc.Config = &types.ForgeConfig{}
	bc.GeneratedFiles["Dockerfile"] = filepath.Join(dir, "Dockerfile")

	stage := &SecretSafetyStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, w := range bc.Warnings {
		if strings.Contains(w, ".dockerignore") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected warning about missing .dockerignore")
	}
}

func TestSecretSafetyStage_DockerignorePresent(t *testing.T) {
	dir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: dir})
	bc.Config = &types.ForgeConfig{}
	bc.GeneratedFiles["Dockerfile"] = filepath.Join(dir, "Dockerfile")

	// Create .dockerignore
	_ = os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(".env\n"), 0644)

	stage := &SecretSafetyStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bc.Warnings) != 0 {
		t.Fatalf("expected no warnings, got: %v", bc.Warnings)
	}
}
