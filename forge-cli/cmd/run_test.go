package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func TestRunCmd_FlagDefaults(t *testing.T) {
	if runPort != 8080 {
		t.Errorf("default port: got %d, want 8080", runPort)
	}
	if runHost != "" {
		t.Errorf("host should default to empty, got %q", runHost)
	}
	if runShutdownTimeout != 0 {
		t.Errorf("shutdown-timeout should default to 0, got %v", runShutdownTimeout)
	}
	if runMockTools {
		t.Error("mock-tools should default to false")
	}
	if !runEnforceGuardrails {
		t.Error("enforce-guardrails should default to true")
	}
	if runNoGuardrails {
		t.Error("no-guardrails should default to false")
	}
	if runModel != "" {
		t.Errorf("model should default to empty, got %q", runModel)
	}
	if runEnvFile != ".env" {
		t.Errorf("env file should default to .env, got %q", runEnvFile)
	}
}

func TestRunCmd_InvalidConfig(t *testing.T) {
	// Create a temp dir with no forge.yaml
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)           //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	err := runRun(nil, nil)
	if err == nil {
		t.Error("expected error for missing config")
	}
}

func TestRunCmd_WithFlagDefault(t *testing.T) {
	if runWithChannels != "" {
		t.Errorf("--with should default to empty, got %q", runWithChannels)
	}
}

func TestRunCmd_InvalidConfigContent(t *testing.T) {
	dir := t.TempDir()

	// Write an invalid forge.yaml (missing required fields)
	cfgContent := "framework: forge\n"
	os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(cfgContent), 0644) //nolint:errcheck

	origDir, _ := os.Getwd()
	os.Chdir(dir)           //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	err := runRun(nil, nil)
	if err == nil {
		t.Error("expected error for invalid config")
	}
}

// TestParseDeferTarget covers the DEFER `to:` routing parse (#310).
func TestParseDeferTarget(t *testing.T) {
	cases := []struct {
		in              string
		adapter, target string
		ok              bool
	}{
		{"channel:slack:#oncall", "slack", "#oncall", true},
		{"channel:telegram:12345", "telegram", "12345", true},
		{"channel:slack:C0123ABC", "slack", "C0123ABC", true},
		{"  channel:slack:#oncall  ", "slack", "#oncall", true}, // trimmed
		{"slack:#oncall", "", "", false},                        // missing scheme
		{"channel:slack", "", "", false},                        // missing target
		{"channel::#oncall", "", "", false},                     // empty adapter
		{"channel:slack:", "", "", false},                       // empty target
		{"", "", "", false},
	}
	for _, c := range cases {
		a, tg, ok := parseDeferTarget(c.in)
		if ok != c.ok || a != c.adapter || tg != c.target {
			t.Errorf("parseDeferTarget(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, a, tg, ok, c.adapter, c.target, c.ok)
		}
	}
}

// TestDeferChannelTargetWarnings covers the #311-review edge case: a DEFER tool
// routing approvals to a channel adapter that isn't active must warn at startup.
func TestDeferChannelTargetWarnings(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled: true,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute":                  {To: "channel:slack:#oncall"}, // slack NOT active → warn
			"atlassian__jira_create_issue": {To: "channel:telegram:123"},  // telegram active → no warn
			"human_tool":                   {To: "human:oncall"},          // non-channel → no warn
		},
	}

	t.Run("warns for inactive adapter, not active ones", func(t *testing.T) {
		warns := deferChannelTargetWarnings(cfg, map[string]bool{"telegram": true})
		if len(warns) != 1 {
			t.Fatalf("expected exactly 1 warning, got %d: %v", len(warns), warns)
		}
		if !strings.Contains(warns[0], "cli_execute") || !strings.Contains(warns[0], `"slack"`) || !strings.Contains(warns[0], "--with slack") {
			t.Errorf("warning wording off: %q", warns[0])
		}
	})

	t.Run("no channels active → warns for the channel target", func(t *testing.T) {
		warns := deferChannelTargetWarnings(cfg, map[string]bool{})
		// slack (inactive) warns; telegram (inactive) warns; human: not a channel.
		if len(warns) != 2 {
			t.Fatalf("expected 2 warnings with no active channels, got %d: %v", len(warns), warns)
		}
	})

	t.Run("all targets active → no warnings", func(t *testing.T) {
		if w := deferChannelTargetWarnings(cfg, map[string]bool{"slack": true, "telegram": true}); len(w) != 0 {
			t.Errorf("expected no warnings, got %v", w)
		}
	})

	t.Run("defer disabled → no warnings", func(t *testing.T) {
		off := cfg
		off.Enabled = false
		if w := deferChannelTargetWarnings(off, map[string]bool{}); w != nil {
			t.Errorf("disabled defer must not warn, got %v", w)
		}
	})
}
