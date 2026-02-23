package security

// DefaultToolDomains maps tool names to their known required domains.
var DefaultToolDomains = map[string][]string{
	"web_search":        {"api.tavily.com", "api.perplexity.ai"},
	"web-search":        {"api.tavily.com", "api.perplexity.ai"},
	"http_request":      {}, // dynamic — depends on user config
	"slack_notify":      {"slack.com", "hooks.slack.com"},
	"github_api":        {"api.github.com", "github.com"},
	"openai_completion": {"api.openai.com"},
	"anthropic_api":     {"api.anthropic.com"},
	"huggingface_api":   {"api-inference.huggingface.co", "huggingface.co"},
	"google_vertex":     {"us-central1-aiplatform.googleapis.com"},
	"sendgrid_email":    {"api.sendgrid.com"},
	"twilio_sms":        {"api.twilio.com"},
	"aws_bedrock":       {"bedrock-runtime.us-east-1.amazonaws.com"},
	"azure_openai":      {"openai.azure.com"},
}

// InferToolDomains looks up known domains for the given tool names and returns a deduplicated list.
func InferToolDomains(toolNames []string) []string {
	seen := make(map[string]bool)
	var domains []string
	for _, name := range toolNames {
		for _, d := range DefaultToolDomains[name] {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	return domains
}
