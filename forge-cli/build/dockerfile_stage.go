package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/initializ/forge/forge-cli/templates"
	"github.com/initializ/forge/forge-core/compiler"
	"github.com/initializ/forge/forge-core/packaging"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
)

// DockerfileStage generates a Dockerfile from the embedded template.
type DockerfileStage struct{}

func (s *DockerfileStage) Name() string { return "generate-dockerfile" }

func (s *DockerfileStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	// Copy project source files into the output directory so they are
	// included in the Docker build context (COPY . .).
	if err := s.copyProjectSources(bc); err != nil {
		return err
	}

	// Copy local binary overrides into the build context
	if err := s.copyLocalBins(bc); err != nil {
		return err
	}

	// Inject local bin overrides into the BinManifest so they get COPY
	// instructions in the generated Dockerfile. This handles binaries
	// not declared by skills (e.g. the forge framework binary itself).
	s.injectLocalBins(bc)

	// Try smart Dockerfile generation when BinManifest is available
	if bc.BinManifest != nil {
		if manifest, ok := bc.BinManifest.(*packaging.BinManifest); ok && len(manifest.Requirements) > 0 {
			if err := s.generateSmartDockerfile(bc, manifest); err != nil {
				return err
			}
			return s.writeDockerignore(bc)
		}
	}

	// Fall through to template-based generation
	if err := s.generateTemplateDockerfile(bc); err != nil {
		return err
	}
	return s.writeDockerignore(bc)
}

func (s *DockerfileStage) generateSmartDockerfile(bc *pipeline.BuildContext, manifest *packaging.BinManifest) error {
	cfg := bc.Config.Package
	binFragment, warnings, err := packaging.GenerateDockerfile(manifest, cfg, bc.PreferAlpine, bc.PreferSlim)
	if err != nil {
		return fmt.Errorf("generating bin install Dockerfile: %w", err)
	}

	// Print resolution progress
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  [bins] warning: %s\n", w)
		bc.AddWarning(w)
	}

	fmt.Fprintf(os.Stderr, "  [bins] resolved %d binaries\n", len(manifest.Requirements))

	// Now generate the main Dockerfile incorporating the bin fragment
	// The bin fragment is a separate stage; we prepend it to the existing template output
	tmplData, err := templates.FS.ReadFile("Dockerfile.tmpl")
	if err != nil {
		return fmt.Errorf("reading Dockerfile template: %w", err)
	}

	tmpl, err := template.New("Dockerfile").Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parsing Dockerfile template: %w", err)
	}

	data := compiler.BuildTemplateDataFromContext(bc.Spec, bc)
	data.HasBinStage = true

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering Dockerfile: %w", err)
	}

	// Combine: bin install stages + main Dockerfile
	var combined bytes.Buffer
	combined.WriteString("# --- Binary installation stages (auto-generated) ---\n")
	combined.WriteString(binFragment)
	combined.WriteString("\n# --- Application stage ---\n")
	combined.Write(buf.Bytes())

	outPath := filepath.Join(bc.Opts.OutputDir, "Dockerfile")
	if err := os.WriteFile(outPath, combined.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}

	bc.AddFile("Dockerfile", outPath)
	return nil
}

func (s *DockerfileStage) generateTemplateDockerfile(bc *pipeline.BuildContext) error {
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

// copyProjectSources copies essential project files from the work directory
// into the build output directory so they are available inside the container.
func (s *DockerfileStage) copyProjectSources(bc *pipeline.BuildContext) error {
	workDir := bc.Opts.WorkDir
	outDir := bc.Opts.OutputDir

	// Individual files to copy
	for _, name := range []string{"forge.yaml"} {
		src := filepath.Join(workDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, filepath.Join(outDir, name)); err != nil {
			return fmt.Errorf("copying %s to output: %w", name, err)
		}
		bc.AddFile(name, filepath.Join(outDir, name))
	}

	// Copy skills/ subdirectory if present
	skillsDir := filepath.Join(workDir, "skills")
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
		if err := copyDir(skillsDir, filepath.Join(outDir, "skills")); err != nil {
			return fmt.Errorf("copying skills/ to output: %w", err)
		}
	}

	return nil
}

// injectLocalBins ensures every local bin override is represented in the
// BinManifest so that the Dockerfile generator emits COPY instructions for them.
func (s *DockerfileStage) injectLocalBins(bc *pipeline.BuildContext) {
	// Collect local bin names from config overrides
	var localNames []string
	if bc.Config != nil {
		for name, override := range bc.Config.Package.BinOverrides {
			if override.LocalPath != "" {
				localNames = append(localNames, name)
			}
		}
	}
	if len(localNames) == 0 {
		return
	}

	// Get or create manifest
	var manifest *packaging.BinManifest
	if bc.BinManifest != nil {
		manifest, _ = bc.BinManifest.(*packaging.BinManifest)
	}
	if manifest == nil {
		manifest = &packaging.BinManifest{
			SkillOrigin: make(map[string]string),
		}
		bc.BinManifest = manifest
	}

	// Build set of existing requirements
	existing := make(map[string]bool)
	for _, req := range manifest.Requirements {
		existing[req.Name] = true
	}

	// Add missing local bins
	for _, name := range localNames {
		if !existing[name] {
			manifest.Requirements = append(manifest.Requirements, contract.BinRequirement{
				Name: name,
			})
			manifest.SkillOrigin[name] = "local-override"
		}
	}
}

// copyLocalBins copies local binary files into .local-bins/ in the build output directory.
// It collects binaries from both forge.yaml config (BinOverrides with LocalPath) and
// CLI flags (bc.LocalBins).
func (s *DockerfileStage) copyLocalBins(bc *pipeline.BuildContext) error {
	// Collect local bins from config
	bins := make(map[string]string)
	if bc.Config != nil {
		for name, override := range bc.Config.Package.BinOverrides {
			if override.LocalPath != "" {
				bins[name] = override.LocalPath
			}
		}
	}
	// CLI flags (bc.LocalBins) may have additional entries not yet in config
	for name, path := range bc.LocalBins {
		bins[name] = path
	}

	if len(bins) == 0 {
		return nil
	}

	localBinsDir := filepath.Join(bc.Opts.OutputDir, ".local-bins")
	if err := os.MkdirAll(localBinsDir, 0755); err != nil {
		return fmt.Errorf("creating .local-bins directory: %w", err)
	}

	for name, src := range bins {
		dst := filepath.Join(localBinsDir, name)
		fmt.Fprintf(os.Stderr, "  [local-bin] copying %s → .local-bins/%s\n", src, name)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copying local binary %s: %w", name, err)
		}
	}

	return nil
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	_, err = io.Copy(out, in)
	return err
}

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func (s *DockerfileStage) writeDockerignore(bc *pipeline.BuildContext) error {
	dockerignoreContent := `.env
.env.*
*.enc
secrets.enc
*.key
*.pem
`
	ignorePath := filepath.Join(bc.Opts.OutputDir, ".dockerignore")
	if err := os.WriteFile(ignorePath, []byte(dockerignoreContent), 0644); err != nil {
		return fmt.Errorf("writing .dockerignore: %w", err)
	}
	bc.AddFile(".dockerignore", ignorePath)
	return nil
}
