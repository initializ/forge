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
	// HOME isolation is no longer required: viableEncryptedFileProviders
	// (issue #52) eagerly validates each candidate file and silently skips
	// any global ~/.forge/secrets.enc that fails to decrypt with the test
	// passphrase. The test now exercises the real chain-build path.
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

// TestOverlaySecretsToEnv_StaleGlobalFileDoesNotPoisonChain is the regression
// for issue #52: a global ~/.forge/secrets.enc encrypted with a different
// passphrase than the project's local file must not block local keys from
// being overlaid. Before the fix, ChainProvider.List() (and Get for keys not
// in the local file) would propagate the decryption error, hiding every
// non-builtin key the local file declared.
func TestOverlaySecretsToEnv_StaleGlobalFileDoesNotPoisonChain(t *testing.T) {
	workDir := t.TempDir()
	fakeHome := t.TempDir()

	const localPass = "local-project-passphrase"
	const globalPass = "old-unrelated-passphrase" // intentionally different
	const customKey = "PROJECT_CUSTOM_KEY"
	const customVal = "from-local-encrypted-file"

	t.Setenv("FORGE_PASSPHRASE", localPass)
	t.Setenv("HOME", fakeHome)
	t.Setenv(customKey, "")

	// Seed the LOCAL encrypted file with the project's passphrase.
	localPath := filepath.Join(workDir, ".forge", "secrets.enc")
	localProvider := secrets.NewEncryptedFileProvider(localPath, func() (string, error) {
		return localPass, nil
	})
	if err := localProvider.SetBatch(map[string]string{customKey: customVal}); err != nil {
		t.Fatalf("seeding local secrets: %v", err)
	}

	// Seed the GLOBAL encrypted file at $HOME/.forge/secrets.enc with a
	// different passphrase. The runtime will pick this file up via the
	// global fallback path; pre-#52 it would poison the chain.
	globalPath := filepath.Join(fakeHome, ".forge", "secrets.enc")
	globalProvider := secrets.NewEncryptedFileProvider(globalPath, func() (string, error) {
		return globalPass, nil
	})
	if err := globalProvider.SetBatch(map[string]string{"UNRELATED": "value"}); err != nil {
		t.Fatalf("seeding global secrets: %v", err)
	}

	cfg := &types.ForgeConfig{
		AgentID: "stale-global-test",
		Secrets: types.SecretsConfig{Providers: []string{"encrypted-file"}},
	}

	OverlaySecretsToEnv(cfg, workDir)

	// The local file's custom key must reach the OS env even though the
	// global file failed to decrypt with the project passphrase.
	if got := os.Getenv(customKey); got != customVal {
		t.Errorf("custom key %q in OS env = %q, want %q (stale global file should be skipped, not poison the chain)", customKey, got, customVal)
	}
}

// TestViableEncryptedFileProviders_Categorization verifies the three outcomes
// for each candidate path: missing file silently skipped, decryptable file
// admitted to the chain, undecryptable file dropped with a warning.
func TestViableEncryptedFileProviders_Categorization(t *testing.T) {
	workDir := t.TempDir()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	const projectPass = "p1"
	const globalPass = "p2"

	// Local: decryptable.
	localPath := filepath.Join(workDir, ".forge", "secrets.enc")
	if err := secrets.NewEncryptedFileProvider(localPath, func() (string, error) {
		return projectPass, nil
	}).SetBatch(map[string]string{"LOCAL_KEY": "v"}); err != nil {
		t.Fatalf("seeding local: %v", err)
	}
	// Global: undecryptable with the project passphrase.
	globalPath := filepath.Join(fakeHome, ".forge", "secrets.enc")
	if err := secrets.NewEncryptedFileProvider(globalPath, func() (string, error) {
		return globalPass, nil
	}).SetBatch(map[string]string{"OTHER_KEY": "v"}); err != nil {
		t.Fatalf("seeding global: %v", err)
	}

	var warnings []string
	got := viableEncryptedFileProviders(workDir, func() (string, error) {
		return projectPass, nil
	}, func(msg string, fields map[string]any) {
		warnings = append(warnings, fields["label"].(string))
	})

	if len(got) != 1 {
		t.Fatalf("got %d viable providers, want 1 (local only)", len(got))
	}
	if len(warnings) != 1 || warnings[0] != "global" {
		t.Errorf("warnings = %v, want one warning for label=global", warnings)
	}
}

// TestViableEncryptedFileProviders_MissingGlobalIsSilent verifies the
// common-case path: no global file at all, no warning, just the local.
func TestViableEncryptedFileProviders_MissingGlobalIsSilent(t *testing.T) {
	workDir := t.TempDir()
	fakeHome := t.TempDir() // no secrets.enc inside
	t.Setenv("HOME", fakeHome)

	const projectPass = "p"
	localPath := filepath.Join(workDir, ".forge", "secrets.enc")
	if err := secrets.NewEncryptedFileProvider(localPath, func() (string, error) {
		return projectPass, nil
	}).SetBatch(map[string]string{"K": "v"}); err != nil {
		t.Fatalf("seeding local: %v", err)
	}

	var warnings []string
	got := viableEncryptedFileProviders(workDir, func() (string, error) {
		return projectPass, nil
	}, func(msg string, fields map[string]any) {
		warnings = append(warnings, fields["label"].(string))
	})

	if len(got) != 1 {
		t.Errorf("got %d providers, want 1", len(got))
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (missing global should be silent)", warnings)
	}
}
