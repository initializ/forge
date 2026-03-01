package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/validate"
	"golang.org/x/term"
)

// loadAndPrepareConfig handles the shared setup logic used by both `run` and `serve`:
// load forge.yaml, validate, resolve workdir, load .env, prompt for passphrase, overlay secrets.
// Returns the loaded config, resolved work directory, and any error.
func loadAndPrepareConfig(envFilePath string) (*types.ForgeConfig, string, error) {
	cfgPath := cfgFile
	if !filepath.IsAbs(cfgPath) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, "", fmt.Errorf("getting working directory: %w", err)
		}
		cfgPath = filepath.Join(wd, cfgPath)
	}

	cfg, err := config.LoadForgeConfig(cfgPath)
	if err != nil {
		return nil, "", fmt.Errorf("loading config: %w", err)
	}

	result := validate.ValidateForgeConfig(cfg)
	if !result.IsValid() {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
		}
		return nil, "", fmt.Errorf("config validation failed: %d error(s)", len(result.Errors))
	}

	workDir := filepath.Dir(cfgPath)

	// Resolve env file path relative to workdir
	envPath := envFilePath
	if !filepath.IsAbs(envPath) {
		envPath = filepath.Join(workDir, envPath)
	}

	// Load .env into process environment so channel adapters can resolve env vars
	envVars, err := runtime.LoadEnvFile(envPath)
	if err != nil {
		return nil, "", fmt.Errorf("loading env file: %w", err)
	}
	for k, v := range envVars {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}

	// Prompt for passphrase if encrypted secrets are configured but passphrase is missing.
	if containsProvider(cfg.Secrets.Providers, "encrypted-file") && os.Getenv("FORGE_PASSPHRASE") == "" {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, "Enter passphrase for encrypted secrets: ")
			raw, pErr := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if pErr == nil && len(raw) > 0 {
				_ = os.Setenv("FORGE_PASSPHRASE", string(raw))
			}
		} else {
			fmt.Fprintln(os.Stderr, "Warning: secrets.providers includes encrypted-file but FORGE_PASSPHRASE is not set; encrypted secrets will not be loaded")
		}
	}

	// Overlay encrypted secrets into OS environment so channel adapters
	// (which use os.Getenv via ResolveEnvVars) can access them.
	runtime.OverlaySecretsToEnv(cfg, workDir)

	return cfg, workDir, nil
}

// parseChannels splits a comma-separated channel string into a slice of trimmed names.
func parseChannels(withChannels string) []string {
	if withChannels == "" {
		return nil
	}
	var channels []string
	for _, name := range strings.Split(withChannels, ",") {
		if n := strings.TrimSpace(name); n != "" {
			channels = append(channels, n)
		}
	}
	return channels
}

// resolveEnvPath returns an absolute path for the env file, resolving relative
// paths against the workDir.
func resolveEnvPath(workDir, envFile string) string {
	if filepath.IsAbs(envFile) {
		return envFile
	}
	return filepath.Join(workDir, envFile)
}

// containsProvider checks whether a string slice contains a given value.
func containsProvider(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
