package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/initializ/forge/forge-core/pipeline"
)

// ManifestStage writes the build-manifest.json with build metadata.
type ManifestStage struct{}

func (s *ManifestStage) Name() string { return "write-build-manifest" }

func (s *ManifestStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	files := make([]string, 0, len(bc.GeneratedFiles))
	for rel := range bc.GeneratedFiles {
		files = append(files, rel)
	}
	sort.Strings(files)

	manifest := map[string]any{
		"agent_id":   bc.Spec.AgentID,
		"version":    bc.Spec.Version,
		"built_at":   time.Now().UTC().Format(time.RFC3339),
		"output_dir": bc.Opts.OutputDir,
		"files":      files,
	}

	// Add container packaging metadata
	if bc.Spec.ForgeVersion != "" {
		manifest["forge_version"] = bc.Spec.ForgeVersion
	}
	if bc.Spec.ToolInterfaceVersion != "" {
		manifest["tool_interface_version"] = bc.Spec.ToolInterfaceVersion
	}
	if bc.Spec.SkillsSpecVersion != "" {
		manifest["skills_spec_version"] = bc.Spec.SkillsSpecVersion
	}
	if bc.SkillsCount > 0 {
		manifest["skills_count"] = bc.SkillsCount
	}
	if bc.Spec.EgressProfile != "" {
		manifest["egress_profile"] = bc.Spec.EgressProfile
	}
	if bc.Spec.EgressMode != "" {
		manifest["egress_mode"] = bc.Spec.EgressMode
	}
	if bc.DevMode {
		manifest["dev_build"] = true
	}
	if len(bc.ToolCategoryCounts) > 0 {
		manifest["tool_categories"] = bc.ToolCategoryCounts
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling build manifest: %w", err)
	}

	outPath := filepath.Join(bc.Opts.OutputDir, "build-manifest.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing build-manifest.json: %w", err)
	}

	bc.AddFile("build-manifest.json", outPath)
	return nil
}
