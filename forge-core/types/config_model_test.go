package types

import "testing"

func TestParseForgeConfig_MaxTokens(t *testing.T) {
	cfg, err := ParseForgeConfig([]byte(`agent_id: max-tokens
version: 0.1.0
framework: forge
model: {provider: anthropic, name: x, max_tokens: 16000}`))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	if cfg.Model.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want 16000", cfg.Model.MaxTokens)
	}
}
