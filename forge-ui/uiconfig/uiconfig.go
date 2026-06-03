// Package uiconfig holds the workspace-level configuration that the
// forge ui process consumes — independent of any specific agent's
// forge.yaml.
//
// Today the only such configuration is the skill-builder LLM (see
// SkillBuilderLLM). The skill builder is a workspace-level activity:
// an operator might build a shared skill before any agent exists, or
// build one skill they will drop into several agents. Tying its
// credentials to a picked agent — which is what the UI did before
// issue #92 — conflates "this agent's runtime LLM" with "the build-
// time codegen LLM I use to author skills" and produces a string of
// problems documented in that issue.
//
// The loader at LoadSkillBuilderLLM resolves the configuration through
// a three-tier precedence:
//
//  1. <workspace>/.forge/ui.yaml — primary, per-workspace.
//  2. ~/.forge/ui.yaml          — fallback, operator's machine-wide.
//  3. The picked agent's forge.yaml + .env — last-resort compat,
//     deprecated. The loader returns Source="agent_fallback" with
//     Warning set so the UI banner can prompt the operator to
//     configure workspace settings.
//
// Stage 3 keeps existing workflows alive during the transition; we
// can drop it after one release cycle.
package uiconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// File names + on-disk layout.
const (
	// WorkspaceConfigDir is the per-workspace directory we look in.
	WorkspaceConfigDir = ".forge"
	// UIConfigFileName is the filename for the workspace-level config.
	UIConfigFileName = "ui.yaml"
	// UserConfigDirName is the directory under the user's home where
	// the fallback config lives. Matches `.forge` for symmetry with
	// the workspace dir.
	UserConfigDirName = ".forge"
)

// Source identifies which resolution tier the loader picked.
const (
	SourceWorkspace     = "workspace"
	SourceUser          = "user"
	SourceAgentFallback = "agent_fallback"
	SourceUnset         = "unset"
)

// File is the on-disk shape of <workspace>/.forge/ui.yaml. Future
// workspace-level config sections live here too.
type File struct {
	SkillBuilder *SkillBuilderConfig `yaml:"skill_builder,omitempty"`
}

// SkillBuilderConfig is the YAML shape of the skill-builder LLM
// configuration that gets persisted. SkillBuilderLLM (below) is the
// resolved runtime view of this, augmented with the actual API key
// looked up from APIKeyEnv.
type SkillBuilderConfig struct {
	// Provider names the LLM provider — one of openai, anthropic,
	// gemini, ollama. Required.
	Provider string `yaml:"provider" json:"provider"`
	// Model is the operator-chosen model name. Required. No hardcoded
	// upgrade is applied at request time (issue #92 explicitly
	// removed the SkillBuilderCodegenModel mapping that forced
	// gpt-4.1 / claude-opus-4-6).
	Model string `yaml:"model" json:"model"`
	// BaseURL is an optional OpenAI-compatible endpoint URL (for
	// OpenRouter, vLLM, litellm, etc.). When set with provider=openai,
	// requests are routed here rather than the openai.com default.
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable the UI process reads
	// for the API key. Defaults per provider (OPENAI_API_KEY,
	// ANTHROPIC_API_KEY, GEMINI_API_KEY). Override if the operator
	// keeps credentials under a different name (e.g.
	// WORKSPACE_LLM_API_KEY).
	APIKeyEnv string `yaml:"api_key_env,omitempty" json:"api_key_env,omitempty"`
}

// SkillBuilderLLM is the resolved-at-request-time view of a
// SkillBuilderConfig. It carries the resolved APIKey but is intended
// to be request-scoped — callers MUST NOT cache it across requests or
// stash the APIKey in process env (which is what the pre-#92 handlers
// did via os.Setenv).
type SkillBuilderLLM struct {
	Provider  string
	Model     string
	BaseURL   string
	APIKeyEnv string
	APIKey    string
	// Source records which tier of the precedence ladder this came
	// from (workspace, user, agent_fallback, unset). The UI surfaces
	// this so operators understand which file to edit + when the
	// agent_fallback deprecation prompt should fire.
	Source string
	// Warning, when non-empty, is a human-readable note the UI should
	// display alongside the resolved config (e.g. the agent-fallback
	// deprecation message).
	Warning string
}

// HasCredentials reports whether the resolved configuration carries
// a usable API key (or is provider=ollama, which doesn't need one).
func (s SkillBuilderLLM) HasCredentials() bool {
	if s.Provider == "ollama" {
		return true
	}
	return s.APIKey != "" && s.APIKey != "__oauth__"
}

// LoadSkillBuilderLLM resolves the skill-builder LLM through the
// three-tier precedence. workspaceDir is the directory `forge ui`
// was launched against (the same workspace the agent scanner walks).
// agentDir is optional; when non-empty AND no workspace/user config
// exists, the loader falls back to reading the agent's forge.yaml +
// .env shape and surfaces the deprecation warning.
//
// envLookup is injected so tests can stub os.Getenv. Production
// callers pass os.Getenv directly.
func LoadSkillBuilderLLM(workspaceDir, agentDir string, envLookup func(string) string) (SkillBuilderLLM, error) {
	if envLookup == nil {
		envLookup = os.Getenv
	}

	// Tier 1: workspace config.
	if cfg, ok, err := readSkillBuilderConfig(filepath.Join(workspaceDir, WorkspaceConfigDir, UIConfigFileName)); err != nil {
		return SkillBuilderLLM{}, fmt.Errorf("workspace ui.yaml: %w", err)
	} else if ok {
		return resolve(cfg, SourceWorkspace, "", envLookup), nil
	}

	// Tier 2: user config.
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, UserConfigDirName, UIConfigFileName)
		if cfg, ok, err := readSkillBuilderConfig(userPath); err != nil {
			return SkillBuilderLLM{}, fmt.Errorf("user ui.yaml: %w", err)
		} else if ok {
			return resolve(cfg, SourceUser, "", envLookup), nil
		}
	}

	// Tier 3: agent fallback. Deprecated; warn loudly so operators
	// migrate. Only fires when an agent context exists.
	if agentDir != "" {
		if cfg, ok := readAgentFallback(agentDir, envLookup); ok {
			warning := "Skill builder is using the selected agent's LLM credentials. " +
				"This fallback is deprecated and will be removed in a future release. " +
				"Configure workspace-level skill-builder LLM under Settings → Skill Builder."
			return resolve(cfg, SourceAgentFallback, warning, envLookup), nil
		}
	}

	return SkillBuilderLLM{Source: SourceUnset}, nil
}

// SaveSkillBuilderLLM persists the skill-builder configuration to
// <workspace>/.forge/ui.yaml. Creates the directory if missing.
// Overwrites any existing file (preserving non-skill-builder sections
// of the File struct — important once other workspace-level sections
// are added).
func SaveSkillBuilderLLM(workspaceDir string, cfg SkillBuilderConfig) error {
	if err := validateSkillBuilderConfig(cfg); err != nil {
		return err
	}

	dir := filepath.Join(workspaceDir, WorkspaceConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, UIConfigFileName)

	// Note: this rewrites the whole file from the typed File struct.
	// When other workspace-level sections are added to ui.yaml (beyond
	// skill_builder), this needs to switch to a yaml.Node-based
	// mutation so unknown sections survive a Save. For v1, skill_builder
	// is the only section so simple typed marshal is correct.
	existing := File{SkillBuilder: &cfg}

	out, err := yaml.Marshal(&existing)
	if err != nil {
		return fmt.Errorf("marshaling ui.yaml: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// readSkillBuilderConfig reads + parses the given path. Returns
// (cfg, false, nil) when the file is absent (a non-error condition
// — fall through to the next tier).
func readSkillBuilderConfig(path string) (SkillBuilderConfig, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SkillBuilderConfig{}, false, nil
		}
		return SkillBuilderConfig{}, false, err
	}
	var file File
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return SkillBuilderConfig{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	if file.SkillBuilder == nil {
		return SkillBuilderConfig{}, false, nil
	}
	return *file.SkillBuilder, true, nil
}

// readAgentFallback approximates the pre-#92 behavior — read the
// agent's forge.yaml model block + .env. Returns ok=false when the
// agent dir lacks a parseable config.
//
// This deliberately uses a minimal struct rather than depending on
// the full types.ForgeConfig + runtime overlay machinery — we only
// need provider/model/base_url/api_key_env to build the same shape
// the workspace tier produces.
func readAgentFallback(agentDir string, envLookup func(string) string) (SkillBuilderConfig, bool) {
	cfgPath := filepath.Join(agentDir, "forge.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return SkillBuilderConfig{}, false
	}
	var legacy struct {
		Model struct {
			Provider string `yaml:"provider"`
			Name     string `yaml:"name"`
		} `yaml:"model"`
	}
	if err := yaml.Unmarshal(raw, &legacy); err != nil {
		return SkillBuilderConfig{}, false
	}
	if legacy.Model.Provider == "" {
		return SkillBuilderConfig{}, false
	}
	// Try to read base_url from the agent's .env (post-#83 wiring).
	// We don't os.Setenv here; just read the file directly.
	cfg := SkillBuilderConfig{
		Provider: legacy.Model.Provider,
		Model:    legacy.Model.Name,
	}
	if env, err := readDotEnv(filepath.Join(agentDir, ".env")); err == nil {
		switch cfg.Provider {
		case "openai":
			if v, ok := env["OPENAI_BASE_URL"]; ok {
				cfg.BaseURL = v
			}
		}
	}
	// APIKeyEnv defaulting happens in resolve(); leave empty here.
	_ = envLookup
	return cfg, true
}

// readDotEnv parses a KEY=VALUE .env file. Minimal implementation —
// no quoting, no exports, no interpolation. Sufficient for the
// fallback path; we don't want to import the heavier runtime parser
// here (would create an import cycle through forge-cli).
func readDotEnv(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range splitLines(string(raw)) {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		eq := indexByte(line, '=')
		if eq <= 0 {
			continue
		}
		out[trimSpace(line[:eq])] = trimSpace(line[eq+1:])
	}
	return out, nil
}

// resolve fills in the runtime view from a persisted config. Sets the
// APIKey by looking up APIKeyEnv (or the provider default) via the
// injected envLookup.
func resolve(cfg SkillBuilderConfig, source, warning string, envLookup func(string) string) SkillBuilderLLM {
	out := SkillBuilderLLM{
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		BaseURL:   cfg.BaseURL,
		APIKeyEnv: cfg.APIKeyEnv,
		Source:    source,
		Warning:   warning,
	}
	if out.APIKeyEnv == "" {
		out.APIKeyEnv = defaultAPIKeyEnv(cfg.Provider)
	}
	if out.APIKeyEnv != "" {
		out.APIKey = envLookup(out.APIKeyEnv)
	}
	return out
}

// defaultAPIKeyEnv returns the conventional env var name for each
// known provider. Empty for ollama (no key needed) and unknowns.
func defaultAPIKeyEnv(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	}
	return ""
}

// ValidateSkillBuilderConfig is exported for the settings HTTP
// handler so the same rules apply to file load + API write.
func ValidateSkillBuilderConfig(cfg SkillBuilderConfig) error {
	return validateSkillBuilderConfig(cfg)
}

func validateSkillBuilderConfig(cfg SkillBuilderConfig) error {
	switch cfg.Provider {
	case "openai", "anthropic", "gemini", "ollama":
	case "":
		return fmt.Errorf("provider is required")
	default:
		return fmt.Errorf("unknown provider %q (must be openai, anthropic, gemini, or ollama)", cfg.Provider)
	}
	if cfg.Model == "" {
		return fmt.Errorf("model is required")
	}
	// base_url only meaningful for OpenAI-compatible setups.
	if cfg.BaseURL != "" && cfg.Provider != "openai" {
		return fmt.Errorf("base_url is only meaningful with provider=openai (got %q)", cfg.Provider)
	}
	// api_key_env, when set, must look like an env var name.
	if cfg.APIKeyEnv != "" {
		for _, c := range cfg.APIKeyEnv {
			if !isEnvNameChar(c) {
				return fmt.Errorf("api_key_env %q contains invalid character %q", cfg.APIKeyEnv, c)
			}
		}
	}
	return nil
}

func isEnvNameChar(c rune) bool {
	return c == '_' ||
		(c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9')
}

// Tiny utility shims used only by readDotEnv. Avoiding a strings
// import to keep this package's dep surface minimal.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
