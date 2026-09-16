package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/settings"
)

func TestChannelsEnabledBySettings(t *testing.T) {
	cases := []struct {
		name      string
		requested []string
		enabled   []string
		wantErr   bool
	}{
		{"empty enabled = unconstrained", []string{"slack", "telegram"}, nil, false},
		{"requested in allowlist", []string{"slack"}, []string{"slack", "telegram"}, false},
		{"all requested in allowlist", []string{"slack", "telegram"}, []string{"slack", "telegram"}, false},
		{"requested not in allowlist", []string{"slack"}, []string{"telegram"}, true},
		{"one of several not enabled", []string{"slack", "msteams"}, []string{"slack"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := channelsEnabledBySettings(tc.requested, tc.enabled)
			if tc.wantErr != (err != nil) {
				t.Fatalf("channelsEnabledBySettings(%v, %v) err=%v, wantErr=%v", tc.requested, tc.enabled, err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "not enabled in settings") {
				t.Errorf("error should explain the gate: %v", err)
			}
		})
	}
}

// forge channel add refuses an adapter not in settings.channels.enabled and
// scaffolds one that is (or when unconstrained).
func TestChannelAdd_GatedBySettings(t *testing.T) {
	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()

	writeUserSettings := func(t *testing.T, dir, body string) {
		t.Helper()
		p := filepath.Join(dir, "user-settings.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		t.Setenv(settings.EnvUserSettings, p)
		// Neutralize the real managed layer for hermeticity.
		settings.SetManagedDirForTest(filepath.Join(dir, "no-managed"))
	}

	t.Run("disabled adapter refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		writeUserSettings(t, dir, `{"channels":{"enabled":["telegram"]}}`)

		err := runChannelAdd(channelAddCmd, []string{"slack"})
		if err == nil || !strings.Contains(err.Error(), "not enabled in settings") {
			t.Fatalf("expected refusal for disabled adapter, got %v", err)
		}
		// Nothing scaffolded.
		if _, statErr := os.Stat(filepath.Join(dir, "slack-config.yaml")); statErr == nil {
			t.Error("slack-config.yaml must not be written for a disabled adapter")
		}
	})

	t.Run("enabled adapter scaffolds", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		writeUserSettings(t, dir, `{"channels":{"enabled":["slack"]}}`)

		if err := runChannelAdd(channelAddCmd, []string{"slack"}); err != nil {
			t.Fatalf("enabled adapter should scaffold: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "slack-config.yaml")); statErr != nil {
			t.Errorf("slack-config.yaml should be written for an enabled adapter: %v", statErr)
		}
	})
}

// forge channel serve (the standalone runner) must honor channels.enabled too,
// else a non-enabled adapter could still be started via that path (#458 review).
func TestChannelServe_GatedBySettings(t *testing.T) {
	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()

	t.Run("disabled adapter refused before start", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "user.json")
		if err := os.WriteFile(p, []byte(`{"channels":{"enabled":["telegram"]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(settings.EnvUserSettings, p)
		settings.SetManagedDirForTest(filepath.Join(dir, "no-managed"))

		err := runChannelServe(channelServeCmd, []string{"slack"})
		if err == nil || !strings.Contains(err.Error(), "not enabled in settings") {
			t.Fatalf("channel serve of a disabled adapter must be refused by the enablement gate, got %v", err)
		}
	})

	t.Run("enabled adapter passes the gate", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "user.json")
		if err := os.WriteFile(p, []byte(`{"channels":{"enabled":["slack"]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(settings.EnvUserSettings, p)
		settings.SetManagedDirForTest(filepath.Join(dir, "no-managed"))

		// No slack-config.yaml / AGENT_URL here, so serve fails LATER — the
		// point is it must NOT fail with the enablement error (the gate passed).
		err := runChannelServe(channelServeCmd, []string{"slack"})
		if err != nil && strings.Contains(err.Error(), "not enabled in settings") {
			t.Fatalf("enabled adapter must pass the gate; got enablement error: %v", err)
		}
	})
}
