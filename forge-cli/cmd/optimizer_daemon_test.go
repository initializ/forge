package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/settings"
)

// TestPickUpstream covers the pure precedence: managed > flag > env > user > chaining.
func TestPickUpstream(t *testing.T) {
	cases := []struct {
		name                               string
		managed, flag, env, user, chaining string
		want                               string
	}{
		{"managed wins over everything (enforced)", "https://managed", "https://flag", "https://env", "https://user", "https://chain", "https://managed"},
		{"flag when no managed", "", "https://flag", "https://env", "https://user", "https://chain", "https://flag"},
		{"env when no flag/managed", "", "", "https://env", "https://user", "https://chain", "https://env"},
		{"user settings when no flag/env/managed", "", "", "", "https://user", "https://chain", "https://user"},
		{"chaining last", "", "", "", "", "https://chain", "https://chain"},
		{"nothing → empty", "", "", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickUpstream(c.managed, c.flag, c.env, c.user, c.chaining); got != c.want {
				t.Errorf("pickUpstream(%q,%q,%q,%q,%q) = %q, want %q",
					c.managed, c.flag, c.env, c.user, c.chaining, got, c.want)
			}
		})
	}
}

// isolateSettings points the user + managed settings layers at empty temp
// locations and clears the env tier, so a test's resolution is deterministic
// and can't pick up the developer's real ~/.forge/settings.json.
func isolateSettings(t *testing.T) (userPath, managedDir string) {
	t.Helper()
	dir := t.TempDir()
	userPath = filepath.Join(dir, "user-settings.json")
	managedDir = t.TempDir()
	t.Setenv(settings.EnvUserSettings, userPath)
	t.Setenv("FORGE_OPTIMIZER_UPSTREAM", "")
	restore := settings.SetManagedDirForTest(managedDir)
	t.Cleanup(restore)
	return userPath, managedDir
}

func writeSettings(t *testing.T, path, upstream string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"optimizer":{"upstream":"`+upstream+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestResolveOptimizerUpstream_UserSettings: optimizer.upstream in
// ~/.forge/settings.json is used when no flag/env/managed set it, but a flag
// still overrides the user layer.
func TestResolveOptimizerUpstream_UserSettings(t *testing.T) {
	userPath, _ := isolateSettings(t)
	writeSettings(t, userPath, "https://user.example")

	if got := resolveOptimizerUpstream("", "", ""); got != "https://user.example" {
		t.Errorf("user settings upstream = %q, want https://user.example", got)
	}
	if got := resolveOptimizerUpstream("https://flag", "", ""); got != "https://flag" {
		t.Errorf("flag should override user settings, got %q", got)
	}
}

// TestResolveOptimizerUpstream_ManagedEnforced: a managed optimizer.upstream
// overrides even an explicit --upstream flag (enterprise enforcement).
func TestResolveOptimizerUpstream_ManagedEnforced(t *testing.T) {
	userPath, managedDir := isolateSettings(t)
	writeSettings(t, userPath, "https://user.example")
	writeSettings(t, filepath.Join(managedDir, "managed-settings.json"), "https://managed.example")

	if got := resolveOptimizerUpstream("https://flag", "https://chain", "127.0.0.1:8787"); got != "https://managed.example" {
		t.Errorf("managed should win over flag, got %q", got)
	}
}

// TestValidateUpstream: malformed, wrong-scheme, hostless, and self-referencing
// values are rejected with a clear error; empty and good URLs pass.
func TestValidateUpstream(t *testing.T) {
	const listen = "127.0.0.1:8787"
	good := []string{"", "https://kong.example", "http://gw.internal:8443/bedrock"}
	for _, u := range good {
		if err := validateUpstream(u, listen); err != nil {
			t.Errorf("validateUpstream(%q) unexpected error: %v", u, err)
		}
	}
	bad := map[string]string{
		"not a url":            "://nope",
		"ftp scheme":           "ftp://host",
		"no host":              "https://",
		"self-loop (explicit)": "http://" + listen,
		"self-loop with path":  "http://" + listen + "/bedrock",
	}
	for name, u := range bad {
		if err := validateUpstream(u, listen); err == nil {
			t.Errorf("%s: validateUpstream(%q) should have errored", name, u)
		}
	}
}

// TestResolveChildUpstream_SelfLoopGuard: the daemon's chaining fallback drops a
// Claude Code settings gateway that points back at our own listen address.
func TestResolveChildUpstream_SelfLoopGuard(t *testing.T) {
	const listen = "127.0.0.1:8787"
	isolateSettings(t) // no user/managed/env upstream configured

	if got := resolveChildUpstream("", "https://kong.example/bedrock", listen); got != "https://kong.example/bedrock" {
		t.Errorf("gateway chaining = %q, want the gateway", got)
	}
	if got := resolveChildUpstream("", "http://"+listen, listen); got != "" {
		t.Errorf("self-pointing settings should be ignored, got %q", got)
	}
	if got := resolveChildUpstream("https://flag", "http://"+listen, listen); got != "https://flag" {
		t.Errorf("flag should win, got %q", got)
	}
}
