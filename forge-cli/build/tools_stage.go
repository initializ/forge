package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
)

// ToolsStage generates tool schema files for each tool in the spec.
type ToolsStage struct{}

func (s *ToolsStage) Name() string { return "generate-tool-schemas" }

func (s *ToolsStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if len(bc.Spec.Tools) == 0 {
		return nil
	}

	toolsDir := filepath.Join(bc.Opts.OutputDir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		return fmt.Errorf("creating tools directory: %w", err)
	}

	for _, tool := range bc.Spec.Tools {
		schema := map[string]any{
			"$schema":     "http://json-schema.org/draft-07/schema#",
			"title":       tool.Name,
			"description": tool.Description,
			"type":        "object",
			"properties":  map[string]any{},
		}

		if len(tool.InputSchema) > 0 {
			var embedded map[string]any
			if err := json.Unmarshal(tool.InputSchema, &embedded); err == nil {
				schema["properties"] = embedded
			}
		}

		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling tool schema %s: %w", tool.Name, err)
		}

		filename := tool.Name + ".schema.json"
		outPath := filepath.Join(toolsDir, filename)
		if err := os.WriteFile(outPath, data, 0644); err != nil {
			return fmt.Errorf("writing tool schema %s: %w", tool.Name, err)
		}

		bc.AddFile(filepath.Join("tools", filename), outPath)
	}

	return nil
}
