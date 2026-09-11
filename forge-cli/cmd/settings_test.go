package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/settings"
)

// `forge settings show` (human view) must mask env VALUES — env is the field
// most likely to carry a secret — while --json emits them verbatim for the
// operator's own dump.
func TestSettingsShow_MasksEnvValuesInHumanView(t *testing.T) {
	dir := t.TempDir()
	userFile := filepath.Join(dir, "user.json")
	if err := os.WriteFile(userFile, []byte(`{"env":{"SECRET_TOKEN":"s3cr3t-value"}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(settings.EnvUserSettings, userFile)
	t.Setenv(settings.EnvManagedSettings, filepath.Join(dir, "no-managed.json"))

	// Human view: value masked, key shown.
	var human bytes.Buffer
	settingsShowCmd.SetOut(&human)
	settingsShowCmd.SetArgs([]string{})
	if err := settingsShowRun(settingsShowCmd, nil); err != nil {
		t.Fatalf("show: %v", err)
	}
	h := human.String()
	if strings.Contains(h, "s3cr3t-value") {
		t.Errorf("human view leaked the env value:\n%s", h)
	}
	if !strings.Contains(h, "SECRET_TOKEN") || !strings.Contains(h, "***") {
		t.Errorf("human view should show the key + masked value:\n%s", h)
	}

	// --json: verbatim (operator's own machine-readable dump).
	var jsonOut bytes.Buffer
	settingsShowCmd.SetOut(&jsonOut)
	_ = settingsShowCmd.Flags().Set("json", "true")
	defer func() { _ = settingsShowCmd.Flags().Set("json", "false") }()
	if err := settingsShowRun(settingsShowCmd, nil); err != nil {
		t.Fatalf("show --json: %v", err)
	}
	if !strings.Contains(jsonOut.String(), "s3cr3t-value") {
		t.Errorf("--json should emit the raw value:\n%s", jsonOut.String())
	}
}

func TestMaskEnvValues(t *testing.T) {
	if got := maskEnvValues(nil); got != nil {
		t.Errorf("nil env → nil, got %v", got)
	}
	got := maskEnvValues(map[string]string{"A": "x", "B": "y"})
	if got["A"] != "***" || got["B"] != "***" {
		t.Errorf("values should be masked, got %v", got)
	}
}
