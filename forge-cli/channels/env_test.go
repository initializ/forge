package channels

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeChannelConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name+"-config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestEnvVarsFromConfig_ExtractsEnvSuffixSettings(t *testing.T) {
	dir := t.TempDir()
	writeChannelConfig(t, dir, "slack", `
adapter: slack
settings:
  app_token_env: SLACK_APP_TOKEN
  bot_token_env: SLACK_BOT_TOKEN
`)
	writeChannelConfig(t, dir, "telegram", `
adapter: telegram
settings:
  bot_token_env: TELEGRAM_BOT_TOKEN
  mode: polling
`)

	got, missing, err := EnvVarsFromConfig(dir, []string{"slack", "telegram"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	want := []string{"SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"}
	if !slices.Equal(got, want) {
		t.Errorf("EnvVarsFromConfig = %v, want %v", got, want)
	}
}

func TestEnvVarsFromConfig_IgnoresNonEnvSettings(t *testing.T) {
	dir := t.TempDir()
	writeChannelConfig(t, dir, "telegram", `
adapter: telegram
settings:
  bot_token_env: TELEGRAM_BOT_TOKEN
  mode: polling
  webhook_path: /tg
`)

	got, _, err := EnvVarsFromConfig(dir, []string{"telegram"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the _env-suffix key should be picked up; mode/webhook_path are not env names.
	if !slices.Equal(got, []string{"TELEGRAM_BOT_TOKEN"}) {
		t.Errorf("got %v, want [TELEGRAM_BOT_TOKEN] (non-env settings should be ignored)", got)
	}
}

func TestEnvVarsFromConfig_DedupsAcrossChannels(t *testing.T) {
	dir := t.TempDir()
	// Two channels declaring the same env var name (e.g. two slack-like
	// adapters sharing a credential). The union must dedup.
	writeChannelConfig(t, dir, "slack", `
adapter: slack
settings:
  bot_token_env: SHARED_TOKEN
`)
	writeChannelConfig(t, dir, "slack-replica", `
adapter: slack
settings:
  bot_token_env: SHARED_TOKEN
`)

	got, _, err := EnvVarsFromConfig(dir, []string{"slack", "slack-replica"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(got, []string{"SHARED_TOKEN"}) {
		t.Errorf("dedup failed: got %v", got)
	}
}

func TestEnvVarsFromConfig_MissingFileReported(t *testing.T) {
	dir := t.TempDir()
	writeChannelConfig(t, dir, "telegram", `
adapter: telegram
settings:
  bot_token_env: TELEGRAM_BOT_TOKEN
`)

	got, missing, err := EnvVarsFromConfig(dir, []string{"slack", "telegram"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(missing, []string{"slack"}) {
		t.Errorf("missing = %v, want [slack]", missing)
	}
	if !slices.Equal(got, []string{"TELEGRAM_BOT_TOKEN"}) {
		t.Errorf("got %v, want only telegram's env (slack file missing)", got)
	}
}

func TestEnvVarsFromConfig_EmptyChannels(t *testing.T) {
	got, missing, err := EnvVarsFromConfig(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 || len(missing) != 0 {
		t.Errorf("expected empty results for no channels, got env=%v missing=%v", got, missing)
	}
}

func TestEnvVarsFromConfig_ParseError(t *testing.T) {
	dir := t.TempDir()
	writeChannelConfig(t, dir, "slack", "this is: not: valid: yaml: [")
	_, _, err := EnvVarsFromConfig(dir, []string{"slack"})
	if err == nil {
		t.Fatal("expected parse error for invalid YAML")
	}
}
