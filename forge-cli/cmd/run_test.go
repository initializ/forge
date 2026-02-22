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
	if runMockTools {
		t.Error("mock-tools should default to false")
	}
	if runEnforceGuardrails {
		t.Error("enforce-guardrails should default to false")
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
	os.Chdir(dir) //nolint:errcheck
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
	cfgContent := "framework: custom\n"
	os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(cfgContent), 0644) //nolint:errcheck

	origDir, _ := os.Getwd()
	os.Chdir(dir) //nolint:errcheck
	defer os.Chdir(origDir) //nolint:errcheck

	err := runRun(nil, nil)
	if err == nil {
		t.Error("expected error for invalid config")
	}
}
