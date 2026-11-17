package steps

import (
	"slices"
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tui"
)

// TestWebSearchStep_Apply pins the three meaningful Apply behaviors the #263
// review flagged: skip writes nothing; a chosen provider writes the provider
// env var + BuiltinTools + key; the env-key path writes the provider but no
// key (it was already in the environment).
func TestWebSearchStep_Apply(t *testing.T) {
	t.Run("skip writes nothing", func(t *testing.T) {
		ctx := tui.NewWizardContext()
		s := &WebSearchStep{provider: ""} // "No web search"
		s.Apply(ctx)
		if len(ctx.BuiltinTools) != 0 {
			t.Errorf("skip must not record a builtin tool, got %v", ctx.BuiltinTools)
		}
		if len(ctx.EnvVars) != 0 {
			t.Errorf("skip must not write env vars, got %v", ctx.EnvVars)
		}
	})

	t.Run("chosen provider writes provider + tool + key", func(t *testing.T) {
		ctx := tui.NewWizardContext()
		s := &WebSearchStep{provider: "tavily", keyName: "TAVILY_API_KEY", key: "tvly-secret"}
		s.Apply(ctx)
		if !slices.Contains(ctx.BuiltinTools, "web_search") {
			t.Errorf("expected web_search recorded, got %v", ctx.BuiltinTools)
		}
		if ctx.EnvVars["WEB_SEARCH_PROVIDER"] != "tavily" {
			t.Errorf("WEB_SEARCH_PROVIDER = %q", ctx.EnvVars["WEB_SEARCH_PROVIDER"])
		}
		if ctx.EnvVars["TAVILY_API_KEY"] != "tvly-secret" {
			t.Errorf("expected the key written, got %q", ctx.EnvVars["TAVILY_API_KEY"])
		}
	})

	t.Run("env-key path writes provider but no key", func(t *testing.T) {
		ctx := tui.NewWizardContext()
		// keyFromEnv: the key is already in the environment, so Apply records
		// the provider + tool but must NOT re-write the key value (it's "").
		s := &WebSearchStep{provider: "perplexity", keyName: "PERPLEXITY_API_KEY", key: "", keyFromEnv: true}
		s.Apply(ctx)
		if ctx.EnvVars["WEB_SEARCH_PROVIDER"] != "perplexity" {
			t.Errorf("WEB_SEARCH_PROVIDER = %q", ctx.EnvVars["WEB_SEARCH_PROVIDER"])
		}
		if _, wrote := ctx.EnvVars["PERPLEXITY_API_KEY"]; wrote {
			t.Errorf("env-key path must not write the key, got %v", ctx.EnvVars)
		}
	})
}

// TestWebSearchStep_Keenable pins the keyless-capable path: Keenable can be
// chosen without a key (only WEB_SEARCH_PROVIDER is written), a provided key
// is written as KEENABLE_API_KEY, and its key prompt is skippable while the
// Tavily/Perplexity prompts stay required.
func TestWebSearchStep_Keenable(t *testing.T) {
	t.Run("keyless writes provider only", func(t *testing.T) {
		ctx := tui.NewWizardContext()
		s := &WebSearchStep{provider: "keenable", keyName: webSearchKeyName("keenable")}
		s.Apply(ctx)
		if ctx.EnvVars["WEB_SEARCH_PROVIDER"] != "keenable" {
			t.Errorf("WEB_SEARCH_PROVIDER = %q", ctx.EnvVars["WEB_SEARCH_PROVIDER"])
		}
		if _, wrote := ctx.EnvVars["KEENABLE_API_KEY"]; wrote {
			t.Errorf("keyless Keenable must not write a key, got %v", ctx.EnvVars)
		}
		if !slices.Contains(ctx.BuiltinTools, "web_search") {
			t.Errorf("expected web_search recorded, got %v", ctx.BuiltinTools)
		}
	})

	t.Run("provided key is written", func(t *testing.T) {
		ctx := tui.NewWizardContext()
		s := &WebSearchStep{provider: "keenable", keyName: webSearchKeyName("keenable"), key: "keen-secret"}
		s.Apply(ctx)
		if ctx.EnvVars["KEENABLE_API_KEY"] != "keen-secret" {
			t.Errorf("expected the key written, got %q", ctx.EnvVars["KEENABLE_API_KEY"])
		}
	})

	t.Run("redo to no web search clears the keenable key", func(t *testing.T) {
		ctx := tui.NewWizardContext()
		ctx.EnvVars["KEENABLE_API_KEY"] = "stale"
		ctx.EnvVars["WEB_SEARCH_PROVIDER"] = "keenable"
		s := &WebSearchStep{provider: ""}
		s.Apply(ctx)
		if len(ctx.EnvVars) != 0 {
			t.Errorf("expected stale keenable env cleared, got %v", ctx.EnvVars)
		}
	})

	t.Run("key names", func(t *testing.T) {
		for provider, want := range map[string]string{
			"tavily": "TAVILY_API_KEY", "perplexity": "PERPLEXITY_API_KEY", "keenable": "KEENABLE_API_KEY",
		} {
			if got := webSearchKeyName(provider); got != want {
				t.Errorf("webSearchKeyName(%q) = %q, want %q", provider, got, want)
			}
		}
	})
}
