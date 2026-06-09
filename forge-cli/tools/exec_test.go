package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSkillCommandExecutor_OrgIDInjection(t *testing.T) {
	// Set the env var
	t.Setenv("OPENAI_ORG_ID", "org-test-skill-123")

	e := &SkillCommandExecutor{}

	// Run a command that prints environment variables
	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "OPENAI_ORG_ID=org-test-skill-123") {
		t.Errorf("expected OPENAI_ORG_ID in env output, got: %s", out)
	}
}

func TestSkillCommandExecutor_NoOrgIDWhenUnset(t *testing.T) {
	// Ensure the env var is NOT set
	os.Unsetenv("OPENAI_ORG_ID") //nolint:errcheck

	e := &SkillCommandExecutor{}

	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(out, "OPENAI_ORG_ID") {
		t.Errorf("expected no OPENAI_ORG_ID in env output, got: %s", out)
	}
}

// TestSkillCommandExecutor_ProviderBaseURLs_AlwaysPassed pins the
// Issue #137 invariant: the standard provider base URL env vars
// (OPENAI_BASE_URL, ANTHROPIC_BASE_URL, OLLAMA_BASE_URL,
// GEMINI_BASE_URL) MUST flow to every skill subprocess when they
// are set in the parent env — without each skill having to declare
// them in its SKILL.md env.optional. These are SDK-recognized
// standard variables for redirecting provider-shape API calls to
// compatible hosts (Together.ai, OpenRouter, Groq, Fireworks,
// Anyscale, remote Ollama, etc.). Pre-fix every such skill silently
// hit the wrong (default-OpenAI) endpoint.
//
// Why one test, not four: the always-passed surface is a single
// allowlist; running env-print once with all four set covers the
// invariant without needing a process spawn per variable.
func TestSkillCommandExecutor_ProviderBaseURLs_AlwaysPassed(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://api.together.ai/v1")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic-proxy.internal/v1")
	t.Setenv("OLLAMA_BASE_URL", "http://ollama.svc.cluster.local:11434")
	t.Setenv("GEMINI_BASE_URL", "https://gemini-proxy.internal/v1")

	// Empty EnvVars whitelist — the skill did NOT declare these in its
	// SKILL.md env.optional. The fix must pass them through anyway.
	e := &SkillCommandExecutor{}

	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"OPENAI_BASE_URL=https://api.together.ai/v1",
		"ANTHROPIC_BASE_URL=https://anthropic-proxy.internal/v1",
		"OLLAMA_BASE_URL=http://ollama.svc.cluster.local:11434",
		"GEMINI_BASE_URL=https://gemini-proxy.internal/v1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in skill subprocess env (issue #137); got:\n%s", want, out)
		}
	}
}

// TestSkillCommandExecutor_ProviderBaseURLs_NotEmittedWhenUnset
// confirms the omit-when-empty semantic: if the parent env doesn't
// have one of these vars set, the subprocess env doesn't gain an
// empty-value line for it. Matches the OPENAI_ORG_ID precedent
// above. The fix must be an allowlist, not a hardcoded forward.
func TestSkillCommandExecutor_ProviderBaseURLs_NotEmittedWhenUnset(t *testing.T) {
	os.Unsetenv("OPENAI_BASE_URL")    //nolint:errcheck
	os.Unsetenv("ANTHROPIC_BASE_URL") //nolint:errcheck
	os.Unsetenv("OLLAMA_BASE_URL")    //nolint:errcheck
	os.Unsetenv("GEMINI_BASE_URL")    //nolint:errcheck

	e := &SkillCommandExecutor{}

	out, err := e.Run(context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, forbidden := range []string{
		"OPENAI_BASE_URL=",
		"ANTHROPIC_BASE_URL=",
		"OLLAMA_BASE_URL=",
		"GEMINI_BASE_URL=",
	} {
		// Use a line-anchored check via newline boundaries so we don't
		// false-positive on a substring that appears inside a value.
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, forbidden) {
				t.Errorf("unset provider base URL %s leaked into subprocess env as %q",
					forbidden, line)
			}
		}
	}
}
