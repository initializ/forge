package runtime

import "testing"

func TestSecretCategory(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"OPENAI_API_KEY", "llm"},
		{"ANTHROPIC_API_KEY", "llm"},
		{"GEMINI_API_KEY", "llm"},
		{"LLM_API_KEY", "llm"},
		{"MODEL_API_KEY", "llm"},
		{"TAVILY_API_KEY", "search"},
		{"PERPLEXITY_API_KEY", "search"},
		{"TELEGRAM_BOT_TOKEN", "telegram"},
		{"SLACK_APP_TOKEN", "slack"},
		{"SLACK_BOT_TOKEN", "slack"},
		{"UNKNOWN_KEY", ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := secretCategory(tt.key)
			if got != tt.want {
				t.Errorf("secretCategory(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
