package build

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/initializ/forge/forge-core/compiler"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-cli/templates"
)

// DockerfileStage generates a Dockerfile from the embedded template.
type DockerfileStage struct{}

func (s *DockerfileStage) Name() string { return "generate-dockerfile" }

func (s *DockerfileStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	tmplData, err := templates.FS.ReadFile("Dockerfile.tmpl")
	if err != nil {
		return fmt.Errorf("reading Dockerfile template: %w", err)
	}

	tmpl, err := template.New("Dockerfile").Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parsing Dockerfile template: %w", err)
	}

	data := compiler.BuildTemplateDataFromContext(bc.Spec, bc)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering Dockerfile: %w", err)
	}

	outPath := filepath.Join(bc.Opts.OutputDir, "Dockerfile")
	if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}

	bc.AddFile("Dockerfile", outPath)
	return nil
}
