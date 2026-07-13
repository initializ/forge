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
