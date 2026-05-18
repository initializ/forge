package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/initializ/forge/forge-core/secrets"
	"github.com/initializ/forge/forge-core/types"
)

func TestSecretCategory(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"OPENAI_API_KEY", "llm"},
		{"ANTHROPIC_API_KEY", "llm"},
		{"GEMINI_API_KEY", "llm"},
		{"LLM_API_KEY", "llm"},
		{"MODEL_API_KEY", "llm"},
		{"TAVILY_API_KEY", "search"},
		{"PERPLEXITY_API_KEY", "search"},
		{"TELEGRAM_BOT_TOKEN", "telegram"},
		{"SLACK_APP_TOKEN", "slack"},
		{"SLACK_BOT_TOKEN", "slack"},
		{"UNKNOWN_KEY", ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := secretCategory(tt.key)
			if got != tt.want {
				t.Errorf("secretCategory(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// stubProvider implements secrets.Provider for unit-testing secretOverlayKeys
// without touching the filesystem.
type stubProvider struct {
	keys    []string
	listErr error
	values  map[string]string
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Get(key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", &secrets.ErrSecretNotFound{Key: key, Provider: s.Name()}
}

func (s *stubProvider) List() ([]string, error) {
	return s.keys, s.listErr
}

func TestSecretOverlayKeys_NonEnumerableProvider(t *testing.T) {
	// Providers that cannot enumerate (e.g. EnvProvider) return nil from List.
	// The overlay set must still include the builtin keys.
	p := &stubProvider{keys: nil}

	got, err := secretOverlayKeys(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(builtinSecretKeys) {
		t.Errorf("len = %d, want %d (builtins only)", len(got), len(builtinSecretKeys))
	}
	for _, b := range builtinSecretKeys {
		if !slices.Contains(got, b) {
			t.Errorf("missing builtin key %q", b)
		}
	}
}

func TestSecretOverlayKeys_UnionsBuiltinsAndListed(t *testing.T) {
	// Provider exposes a custom skill-declared key plus a duplicate of a
	// builtin. The result must include the builtin set, plus the custom key,
	// with no duplicates.
	p := &stubProvider{keys: []string{"MY_CUSTOM_KEY", "OPENAI_API_KEY", "ANOTHER_CUSTOM"}}

	got, err := secretOverlayKeys(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, b := range builtinSecretKeys {
		if !slices.Contains(got, b) {
			t.Errorf("missing builtin key %q", b)
		}
	}
	if !slices.Contains(got, "MY_CUSTOM_KEY") {
		t.Errorf("expected custom key MY_CUSTOM_KEY in overlay set")
	}
	if !slices.Contains(got, "ANOTHER_CUSTOM") {
		t.Errorf("expected custom key ANOTHER_CUSTOM in overlay set")
	}

	// Dedup: OPENAI_API_KEY appears once even though both sources declared it.
	count := 0
	for _, k := range got {
		if k == "OPENAI_API_KEY" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("OPENAI_API_KEY appeared %d times, want 1 (dedup failed)", count)
	}
}

func TestSecretOverlayKeys_ListError(t *testing.T) {
	// A failing List() must not lose the builtins — the runner can still
	// recover the builtin keys via Get downstream.
	wantErr := &secrets.ErrSecretNotFound{Key: "passphrase", Provider: "stub"}
	p := &stubProvider{listErr: wantErr}

	got, err := secretOverlayKeys(p)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(got) != len(builtinSecretKeys) {
		t.Errorf("len = %d, want %d (builtins only on List error)", len(got), len(builtinSecretKeys))
	}
}

// TestOverlaySecretsToEnv_CustomSkillKey is the end-to-end regression test
// for issue #48: a skill-declared env var stored only in the encrypted secrets
// file must reach the OS environment so downstream executors can read it.
func TestOverlaySecretsToEnv_CustomSkillKey(t *testing.T) {
	workDir := t.TempDir()
	const passphrase = "test-passphrase"
	const customKey = "MY_SKILL_API_KEY"
	const customVal = "skill-secret-value"
	const builtinKey = "TAVILY_API_KEY"
	const builtinVal = "tavily-secret-value"

	t.Setenv("FORGE_PASSPHRASE", passphrase)
	// Redirect HOME so the global ~/.forge/secrets.enc fallback resolves to an
	// empty path under the temp dir, isolating the test from the developer's
	// real home directory.
	t.Setenv("HOME", t.TempDir())
	// Ensure the target env vars are clear before the test runs.
	t.Setenv(customKey, "")
	t.Setenv(builtinKey, "")

	// Write the encrypted secrets file the same way `forge secrets set` would.
	encPath := filepath.Join(workDir, ".forge", "secrets.enc")
	provider := secrets.NewEncryptedFileProvider(encPath, func() (string, error) {
		return passphrase, nil
	})
	if err := provider.SetBatch(map[string]string{
		customKey:  customVal,
		builtinKey: builtinVal,
	}); err != nil {
		t.Fatalf("seeding encrypted secrets: %v", err)
	}

	cfg := &types.ForgeConfig{
		AgentID: "test-agent",
		Secrets: types.SecretsConfig{Providers: []string{"encrypted-file"}},
	}

	OverlaySecretsToEnv(cfg, workDir)

	// Builtin behavior preserved.
	if got := os.Getenv(builtinKey); got != builtinVal {
		t.Errorf("builtin key %q in OS env = %q, want %q", builtinKey, got, builtinVal)
	}
	// Custom skill key (not in builtinSecretKeys) is now overlaid too — this
	// is the regression check for #48.
	if got := os.Getenv(customKey); got != customVal {
		t.Errorf("custom skill key %q in OS env = %q, want %q", customKey, got, customVal)
	}
}
