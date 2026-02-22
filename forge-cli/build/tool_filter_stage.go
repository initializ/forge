package build

import (
	"context"

	"github.com/initializ/forge/forge-core/pipeline"
)

// Known dev tools that should be filtered in production builds.
var knownDevTools = map[string]bool{
	"local_shell":        true,
	"local_file_browser": true,
	"debug_console":      true,
	"test_runner":        true,
}

// Known builtin tools.
var knownBuiltinTools = map[string]bool{
	"web_search":       true,
	"web-search":       true,
	"http_request":     true,
	"code_interpreter": true,
	"text_generation":  true,
	"cli_execute":      true,
}

// Known adapter tools.
var knownAdapterTools = map[string]bool{
	"slack_notify":      true,
	"github_api":        true,
	"sendgrid_email":    true,
	"twilio_sms":        true,
	"openai_completion": true,
	"anthropic_api":     true,
	"huggingface_api":   true,
	"google_vertex":     true,
	"aws_bedrock":       true,
	"azure_openai":      true,
}

// ToolFilterStage annotates tool categories and filters dev tools in production mode.
type ToolFilterStage struct{}

func (s *ToolFilterStage) Name() string { return "filter-tools" }

func (s *ToolFilterStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if bc.Spec == nil {
		return nil
	}

	bc.Spec.ToolInterfaceVersion = "1.0"

	// Annotate categories
	for i := range bc.Spec.Tools {
		name := bc.Spec.Tools[i].Name
		switch {
		case knownDevTools[name]:
			bc.Spec.Tools[i].Category = "dev"
		case knownBuiltinTools[name]:
			bc.Spec.Tools[i].Category = "builtin"
		case knownAdapterTools[name]:
			bc.Spec.Tools[i].Category = "adapter"
		default:
			bc.Spec.Tools[i].Category = "custom"
		}
	}

	// Filter dev tools in prod mode
	if bc.ProdMode {
		filtered := bc.Spec.Tools[:0]
		for _, t := range bc.Spec.Tools {
			if t.Category != "dev" {
				filtered = append(filtered, t)
			}
		}
		bc.Spec.Tools = filtered
	}

	// Count categories
	counts := make(map[string]int)
	for _, t := range bc.Spec.Tools {
		counts[t.Category]++
	}
	bc.ToolCategoryCounts = counts

	return nil
}
