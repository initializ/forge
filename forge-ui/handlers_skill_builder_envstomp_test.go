package forgeui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestSkillBuilderProvider_DoesNotStompProcessEnvBetweenAgents pins one of
// issue #92's acceptance criteria: switching the selected agent in the UI
// must not modify the forge ui process's environment variables.
//
// Pre-#92 handleSkillBuilderProvider loaded the agent's .env into the UI
// process via os.Setenv (lines 80-89 of the old handler). Picking agent A
// then agent B left A's keys in the process env, then B's keys overwrote
// them — cross-agent leakage of credentials through the shared process
// state.
//
// Post-#92 the handler reads workspace-level config (uiconfig) and never
// touches os.Setenv. This test pins that contract.
func TestSkillBuilderProvider_DoesNotStompProcessEnvBetweenAgents(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()

	// Two agents in the same workspace, with different OPENAI_API_KEY
	// values in their .env files. The pre-#92 handler would have read
	// these into the UI process via os.Setenv on each fetch; we assert
	// no such mutation happens.
	agentA := filepath.Join(root, "agent-a")
	if err := os.MkdirAll(agentA, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentA, "forge.yaml"), `agent_id: agent-a
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)
	writeFile(t, filepath.Join(agentA, ".env"), "OPENAI_API_KEY=key-from-agent-a\n")

	agentB := filepath.Join(root, "agent-b")
	if err := os.MkdirAll(agentB, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentB, "forge.yaml"), `agent_id: agent-b
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)
	writeFile(t, filepath.Join(agentB, ".env"), "OPENAI_API_KEY=key-from-agent-b\n")

	// Snapshot the UI process's env before any handler call. Both agents'
	// keys must remain ABSENT throughout — these are agent secrets, not
	// UI-process secrets.
	for _, k := range []string{"OPENAI_API_KEY"} {
		if _, present := os.LookupEnv(k); present {
			_ = os.Unsetenv(k)
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
	}

	srv := NewUIServer(UIServerConfig{
		Port:    4200,
		WorkDir: root,
	})

	// Fetch provider info for agent A, then agent B. Assert process env
	// is unchanged after EACH call (proving no os.Setenv leaks from the
	// agent's .env into the UI process).
	for _, id := range []string{"agent-a", "agent-b", "agent-a"} {
		req := httptest.NewRequest(http.MethodGet, "/api/agents/"+id+"/skill-builder/provider", nil)
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		srv.handleSkillBuilderProvider(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("agent %q: status = %d, body: %s", id, w.Code, w.Body.String())
		}

		// Decode response to confirm it parsed.
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("agent %q: decode: %v", id, err)
		}

		// The critical assertion: process env must NOT have absorbed
		// either agent's OPENAI_API_KEY. Pre-#92, after the first call
		// this would have been "key-from-agent-a"; after the second
		// "key-from-agent-b". Post-#92, neither must be set.
		got := os.Getenv("OPENAI_API_KEY")
		if got == "key-from-agent-a" || got == "key-from-agent-b" {
			t.Errorf("after fetching agent %q, OPENAI_API_KEY leaked from agent .env into UI process: %q",
				id, got)
		}
	}
}
