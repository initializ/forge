package cmd

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// TestAuthLogout_RemovesCredential writes a fake OAuth credential to an isolated
// dir and confirms `auth logout` deletes it.
func TestAuthLogout_RemovesCredential(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("FORGE_PLATFORM_TOKEN", "") // ensure the guard doesn't trip

	if err := oauth.SaveCredentials("openai", &oauth.Token{AccessToken: "tok", RefreshToken: "r"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if tok, _ := oauth.LoadCredentials("openai"); tok == nil {
		t.Fatal("precondition: credential should exist")
	}

	var out strings.Builder
	authLogoutCmd.SetOut(&out)
	if err := runAuthLogout(authLogoutCmd, []string{"openai"}); err != nil {
		t.Fatalf("runAuthLogout: %v", err)
	}
	if tok, _ := oauth.LoadCredentials("openai"); tok != nil {
		t.Error("credential should be gone after logout")
	}
	if !strings.Contains(out.String(), "Logged out of openai") {
		t.Errorf("output = %q, want a logged-out message", out.String())
	}
}

// TestAuthLogout_NothingToDo reports cleanly when no credential is stored.
func TestAuthLogout_NothingToDo(t *testing.T) {
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("FORGE_PLATFORM_TOKEN", "")

	var out strings.Builder
	authLogoutCmd.SetOut(&out)
	if err := runAuthLogout(authLogoutCmd, nil); err != nil {
		t.Fatalf("runAuthLogout: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("output = %q, want a nothing-to-do message", out.String())
	}
}

// TestAuthLogout_RejectsTraversalProvider pins that an unknown / path-traversal
// provider arg is rejected before it reaches the credential store's
// provider->path mapping (which would otherwise delete <arg>.json anywhere).
func TestAuthLogout_RejectsTraversalProvider(t *testing.T) {
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("FORGE_PLATFORM_TOKEN", "")

	for _, bad := range []string{"../../../../tmp/x", "openai/../../etc/x", "google", "x"} {
		err := runAuthLogout(authLogoutCmd, []string{bad})
		if err == nil || !strings.Contains(err.Error(), "unknown provider") {
			t.Errorf("logout %q: want unknown-provider rejection, got %v", bad, err)
		}
	}
}

// TestAuthLogout_RefusesInAgentRuntime is the guard: with a platform token set
// (a managed deployment), logout refuses and does NOT touch the credential.
func TestAuthLogout_RefusesInAgentRuntime(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	if err := oauth.SaveCredentials("openai", &oauth.Token{AccessToken: "tok"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	t.Setenv("FORGE_PLATFORM_TOKEN", "platform-tok")

	err := runAuthLogout(authLogoutCmd, []string{"openai"})
	if err == nil {
		t.Fatal("want a refusal error inside a managed deployment")
	}
	if !strings.Contains(err.Error(), "refusing to log out") {
		t.Errorf("error = %q, want a refusal", err.Error())
	}
	// The credential must remain untouched.
	if tok, _ := oauth.LoadCredentials("openai"); tok == nil {
		t.Error("logout must not delete the credential when refused")
	}
}
