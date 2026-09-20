package cmd

import (
	"os"
	"path/filepath"
	"testing"
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
		{"user settings.json when no flag/env/managed", "", "", "", "https://user", "https://chain", "https://user"},
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

// TestSettingsUpstream checks the settings.json reader (used for both user and
// managed files) — missing/bad files never fatal, valid file yields upstream.
func TestSettingsUpstream(t *testing.T) {
	if got := settingsUpstream(filepath.Join(t.TempDir(), "nope.json")); got != "" {
		t.Errorf("missing file should be empty, got %q", got)
	}

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsUpstream(bad); got != "" {
		t.Errorf("bad json should be empty, got %q", got)
	}

	good := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(good, []byte(`{"optimizer":{"upstream":"https://kong.example/bedrock"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsUpstream(good); got != "https://kong.example/bedrock" {
		t.Errorf("valid settings upstream = %q", got)
	}
}

// TestResolveOptimizerUpstream_UserSettings drives the full resolver against a
// real ~/.forge/settings.json (HOME redirected to a temp dir), env cleared, and
// no managed file present on the runner.
func TestResolveOptimizerUpstream_UserSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_OPTIMIZER_UPSTREAM", "")
	if err := os.MkdirAll(filepath.Join(home, ".forge"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".forge", "settings.json"),
		[]byte(`{"optimizer":{"upstream":"https://user.example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// No flag, no env, no chaining → user settings wins (managed file absent).
	if got := resolveOptimizerUpstream("", ""); got != "https://user.example" {
		t.Errorf("user settings upstream = %q, want https://user.example", got)
	}
	// Flag still beats user settings.
	if got := resolveOptimizerUpstream("https://flag", ""); got != "https://flag" {
		t.Errorf("flag over user = %q", got)
	}
}

// TestResolveChildUpstream_SelfLoopGuard checks the daemon's chaining fallback
// drops a Claude Code settings gateway that points back at our own listen addr.
func TestResolveChildUpstream_SelfLoopGuard(t *testing.T) {
	const listen = "127.0.0.1:8787"
	// Isolate from ambient config: clear the env tier and point HOME at an empty
	// temp dir so ~/.forge/settings.json can't leak in. (The managed settings
	// file lives at a system path that won't exist on the test runner.)
	t.Setenv("FORGE_OPTIMIZER_UPSTREAM", "")
	t.Setenv("HOME", t.TempDir())

	// A real gateway in settings → used as chaining.
	if got := resolveChildUpstream("", "https://kong.example/bedrock", listen); got != "https://kong.example/bedrock" {
		t.Errorf("gateway chaining = %q, want the gateway", got)
	}
	// Settings pointing back at us → dropped, nothing else configured → "".
	if got := resolveChildUpstream("", "http://"+listen, listen); got != "" {
		t.Errorf("self-pointing settings should be ignored, got %q", got)
	}
	// Explicit flag always wins over chaining.
	if got := resolveChildUpstream("https://flag", "http://"+listen, listen); got != "https://flag" {
		t.Errorf("flag should win, got %q", got)
	}
}
