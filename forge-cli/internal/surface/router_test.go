package surface

import (
	"strings"
	"testing"
)

func TestRouterMatchesIntent(t *testing.T) {
	cases := map[string]string{
		"how do I wire a slack channel":     "channels",
		"what goes in forge.yaml egress":    "forge-yaml",
		"set up a cron schedule":            "scheduling",
		"deploy the agent to kubernetes":    "build-deploy",
		"which model provider should I use": "models",
	}
	for input, want := range cases {
		r := NewRouter()
		got := r.Match(input)
		if !contains(got, want) {
			t.Errorf("Match(%q) = %v, want to include %q", input, got, want)
		}
	}
}

func TestRouterInjectsOncePerSession(t *testing.T) {
	r := NewRouter()
	first := r.Match("slack channel setup")
	if !contains(first, "channels") {
		t.Fatalf("first turn should match channels, got %v", first)
	}
	second := r.Match("more about the slack channel")
	if contains(second, "channels") {
		t.Errorf("channels should not be re-injected, got %v", second)
	}
}

func TestKnowledgePrefaceEmptyWhenNoMatch(t *testing.T) {
	r := NewRouter()
	if p := r.KnowledgePreface("hello there"); p != "" {
		t.Errorf("expected empty preface for non-topical input, got %.60q", p)
	}
	p := r.KnowledgePreface("tell me about mcp servers")
	if !strings.Contains(p, "[forge knowledge") {
		t.Errorf("expected a knowledge block for mcp intent, got %.60q", p)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
