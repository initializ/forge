package compiler

import (
	"github.com/initializ/forge/forge-core/agentspec"
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

// AnnotateToolCategories sets the Category field on each tool based on known tool lists.
func AnnotateToolCategories(tools []agentspec.ToolSpec) {
	for i := range tools {
		name := tools[i].Name
		switch {
		case knownDevTools[name]:
			tools[i].Category = "dev"
		case knownBuiltinTools[name]:
			tools[i].Category = "builtin"
		case knownAdapterTools[name]:
			tools[i].Category = "adapter"
		default:
			tools[i].Category = "custom"
		}
	}
}

// FilterDevTools removes tools with category "dev" from the slice and returns the filtered result.
func FilterDevTools(tools []agentspec.ToolSpec) []agentspec.ToolSpec {
	filtered := make([]agentspec.ToolSpec, 0, len(tools))
	for _, t := range tools {
		if t.Category != "dev" {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// CountToolCategories returns a map of category to count for the given tools.
func CountToolCategories(tools []agentspec.ToolSpec) map[string]int {
	counts := make(map[string]int)
	for _, t := range tools {
		counts[t.Category]++
	}
	return counts
}
