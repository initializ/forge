package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

func TestRunner_FallbackMaxTokens(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["max_tokens"] != float64(2000) {
			t.Errorf("fallback max_tokens = %v, want 2000", body["max_tokens"])
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer fallback.Close()
	cfg := &types.ForgeConfig{Model: types.ModelRef{
		Provider: "openai", Name: "primary", MaxTokens: 16000,
		Fallbacks: []types.ModelFallback{{Provider: "ollama", Name: "fallback", MaxTokens: 2000}},
	}}
	mc := coreruntime.ResolveModelConfig(cfg, map[string]string{
		"OPENAI_API_KEY": "test", "OPENAI_BASE_URL": primary.URL, "OLLAMA_BASE_URL": fallback.URL,
	}, "")
	runner := &Runner{cfg: RunnerConfig{Config: cfg}}
	client, err := runner.buildLLMClient(mc)
	if err != nil {
		t.Fatal(err)
	}
	req := &llm.ChatRequest{MaxTokens: cfg.Model.MaxTokens}
	if _, err := client.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.MaxTokens != 16000 {
		t.Fatal("caller request mutated")
	}
}
