package cmd

import (
	"os"
	"path/filepath"
	"testing"
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
