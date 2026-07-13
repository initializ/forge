package steps

import (
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tui"
)

// TestStepInitResetsComplete pins the re-entry contract from the #264 review:
// every step's Init() must reset `complete` to false, because the wizard calls
// Init() (not Prepare) on BACK navigation and every step's Update short-
// circuits on `if s.complete`. Without the reset, esc-back lands on an inert
// step that ignores all input and soft-locks the wizard.
//
// The test drives REAL steps (a stateless mock can't catch the soft-lock —
// see the wizard_nav_test note): construct the step, force it complete, call
// Init(), and assert it's usable again.
func TestStepInitResetsComplete(t *testing.T) {
	styles := tui.NewStyleSet(tui.DarkTheme)
	noKey := func(string, string) error { return nil }

	t.Run("name", func(t *testing.T) {
		s := NewNameStep(styles, "") // no prefill
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("NameStep.Init() must reset complete")
		}
	})
	t.Run("provider", func(t *testing.T) {
		s := NewProviderStep(styles, noKey)
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("ProviderStep.Init() must reset complete")
		}
	})
	t.Run("channel", func(t *testing.T) {
		s := NewChannelStep(styles)
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("ChannelStep.Init() must reset complete")
		}
	})
	t.Run("web_search", func(t *testing.T) {
		s := NewWebSearchStep(styles, noKey)
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("WebSearchStep.Init() must reset complete")
		}
	})
	t.Run("fallback", func(t *testing.T) {
		s := NewFallbackStep(styles, noKey)
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("FallbackStep.Init() must reset complete")
		}
	})
	t.Run("auth", func(t *testing.T) {
		s := NewAuthStep(styles)
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("AuthStep.Init() must reset complete")
		}
	})
	t.Run("compression", func(t *testing.T) {
		s := NewCompressionStep(styles)
		s.complete = true
		s.Init()
		if s.complete {
			t.Error("CompressionStep.Init() must reset complete")
		}
	})
}

// TestWebSearchStepApply_ClearsStaleKeys pins finding #2: redoing the
// web-search step (after back-nav) with "No web search" chosen must not leak
// the previously-entered provider/key into the context.
func TestWebSearchStepApply_ClearsStaleKeys(t *testing.T) {
	styles := tui.NewStyleSet(tui.DarkTheme)
	ctx := tui.NewWizardContext()

	// First pass: Tavily chosen with a key.
	s := NewWebSearchStep(styles, func(string, string) error { return nil })
	s.provider = "tavily"
	s.keyName = "TAVILY_API_KEY"
	s.key = "tvly-x"
	s.Apply(ctx)
	if ctx.EnvVars["TAVILY_API_KEY"] == "" || ctx.EnvVars["WEB_SEARCH_PROVIDER"] == "" {
		t.Fatal("first Apply should have written the web-search env vars")
	}
	if !containsWebSearch(ctx.BuiltinTools) {
		t.Fatal("first Apply should have recorded web_search")
	}

	// Redo: user went back and chose "No web search".
	s.provider = ""
	s.keyName = ""
	s.key = ""
	s.Apply(ctx)

	if _, ok := ctx.EnvVars["TAVILY_API_KEY"]; ok {
		t.Error("stale TAVILY_API_KEY leaked after choosing No web search")
	}
	if _, ok := ctx.EnvVars["WEB_SEARCH_PROVIDER"]; ok {
		t.Error("stale WEB_SEARCH_PROVIDER leaked after choosing No web search")
	}
	if containsWebSearch(ctx.BuiltinTools) {
		t.Error("stale web_search left in BuiltinTools after deselect")
	}
}

func containsWebSearch(s []string) bool {
	for _, v := range s {
		if v == "web_search" {
			return true
		}
	}
	return false
}
