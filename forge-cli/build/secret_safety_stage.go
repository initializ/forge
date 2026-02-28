package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
)

// SecretSafetyStage validates that container builds won't leak secrets.
type SecretSafetyStage struct{}

func (s *SecretSafetyStage) Name() string { return "secret-safety" }

func (s *SecretSafetyStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	providers := bc.Config.Secrets.Providers

	// Check for encrypted-file-only configuration in prod mode.
	if bc.ProdMode && len(providers) > 0 {
		hasContainerSafe := false
		for _, p := range providers {
			if p == "env" {
				hasContainerSafe = true
				break
			}
		}
		if !hasContainerSafe {
			return fmt.Errorf("production builds require a container-compatible secret provider (env); " +
				"encrypted-file alone cannot inject secrets into containers")
		}
	}

	// Warn if only encrypted-file is configured (non-prod).
	if len(providers) == 1 && providers[0] == "encrypted-file" {
		bc.AddWarning("secrets provider is encrypted-file only; container builds will not have access to secrets at runtime (use env provider for containers)")
	}

	// Verify .dockerignore exists if a Dockerfile was generated.
	if _, hasDockerfile := bc.GeneratedFiles["Dockerfile"]; hasDockerfile {
		ignorePath := filepath.Join(bc.Opts.OutputDir, ".dockerignore")
		if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
			bc.AddWarning(".dockerignore not found alongside Dockerfile; secrets may leak into container image")
		}
	}

	return nil
}
