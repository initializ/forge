package runtime

import "testing"

func TestAgentCardBaseURL(t *testing.T) {
	t.Run("defaults to pod-local localhost when unset", func(t *testing.T) {
		t.Setenv("FORGE_A2A_PUBLIC_URL", "")
		if got := agentCardBaseURL(8080); got != "http://localhost:8080" {
			t.Errorf("got %q, want http://localhost:8080", got)
		}
	})
	t.Run("honors FORGE_A2A_PUBLIC_URL for ingress-fronted agents", func(t *testing.T) {
		t.Setenv("FORGE_A2A_PUBLIC_URL", "https://agent-ws.test.agents.initz.run")
		if got := agentCardBaseURL(8080); got != "https://agent-ws.test.agents.initz.run" {
			t.Errorf("got %q, want the public URL", got)
		}
	})
	t.Run("trims a trailing slash so the card url is clean", func(t *testing.T) {
		t.Setenv("FORGE_A2A_PUBLIC_URL", "https://agent-ws.test.agents.initz.run/")
		if got := agentCardBaseURL(8080); got != "https://agent-ws.test.agents.initz.run" {
			t.Errorf("got %q, want the trailing slash trimmed", got)
		}
	})
}
