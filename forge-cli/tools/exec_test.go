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
