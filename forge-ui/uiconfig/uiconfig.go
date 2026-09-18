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

	"github.com/initializ/forge/forge-core/catalog"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
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
	// SourceGlobalOAuth / SourceGlobalEnv are used when the resolved
	// credentials come from the operator's machine-global forge auth —
	// a stored OAuth token (~/.forge/credentials) or a global API-key
	// env var — rather than from a workspace/user ui.yaml credential.
	// These fire either as a gap-fill under an existing ui.yaml config
	// or as the sole source when no ui.yaml exists at all. See issue #92
	// follow-up: "skill builder should reuse my machine's global auth".
	SourceGlobalOAuth = "global_oauth"
	SourceGlobalEnv   = "global_env"
	// SourceGlobalGateway fires when no ui.yaml exists but the settings
	// model default names a provider that has a configured gateway
	// (enterprise Anthropic / OpenAI). The builder authenticates through
	// that gateway at request time.
	SourceGlobalGateway = "global_gateway"
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
	// UseGlobalAuth is the managed "always use my machine-global forge
	// auth" toggle. When false (default), global auth — a stored OAuth
	// token or a global API-key env var — is only used to FILL A GAP:
	// i.e. when the ui.yaml config resolves no usable API key. When
	// true, global auth takes precedence even if api_key_env resolves,
	// so an operator who has OAuthed (or keeps a single global key) can
	// force the skill/agent builder onto it without clearing per-
	// workspace settings. OAuth is only available for provider=openai
	// today (mirrors the agent runtime's createProviderClient).
	UseGlobalAuth bool `yaml:"use_global_auth,omitempty" json:"use_global_auth,omitempty"`
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
	// UseOAuth signals that credentials come from a stored machine-
	// global OAuth token rather than an API key. When set, the caller
	// (forge-cli's LLMStreamFunc) MUST build an OAuth-aware client that
	// loads + refreshes the token from the credential store at request
	// time — APIKey is intentionally left empty here so a stale token
	// is never baked into the resolved config. Mirrors the agent
	// runtime's createProviderClient OAuth path.
	UseOAuth bool
	// UseGateway signals that a model gateway is configured (in the
	// system/user settings layers) for this provider. The caller (forge-
	// cli's LLMStreamFunc) then authenticates through that gateway —
	// overlaying base_url + auth_scheme (bearer / x-api-key / apikey_header)
	// and minting/injecting the token from ~/.forge/credentials via the
	// gateway's api_key_helper — exactly like `forge run`. This is the
	// enterprise-Anthropic (and OpenAI) login path. When set, credentials
	// are considered available even without an explicit API key or OAuth
	// token, since the gateway supplies them at request time.
	UseGateway bool
	// UseGlobalAuth echoes the persisted managed toggle (SkillBuilderConfig.
	// UseGlobalAuth) so the Settings UI can round-trip it. It does not
	// affect resolution here — LoadSkillBuilderLLM reads the toggle from
	// the config directly — it's purely informational for the UI.
	UseGlobalAuth bool
}

// HasCredentials reports whether the resolved configuration carries
// a usable credential: an API key, an ollama endpoint (no key needed),
// or a machine-global OAuth token the caller will load at request time.
func (s SkillBuilderLLM) HasCredentials() bool {
	if s.Provider == "ollama" {
		return true
	}
	if s.UseOAuth || s.UseGateway {
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
		out := resolve(cfg, SourceWorkspace, "", envLookup)
		applyGlobalAuth(&out, cfg.UseGlobalAuth, envLookup)
		markGateway(&out, workspaceDir)
		return out, nil
	}

	// Tier 2: user config.
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, UserConfigDirName, UIConfigFileName)
		if cfg, ok, err := readSkillBuilderConfig(userPath); err != nil {
			return SkillBuilderLLM{}, fmt.Errorf("user ui.yaml: %w", err)
		} else if ok {
			out := resolve(cfg, SourceUser, "", envLookup)
			applyGlobalAuth(&out, cfg.UseGlobalAuth, envLookup)
			markGateway(&out, workspaceDir)
			return out, nil
		}
	}

	// Tier 3: agent fallback. Deprecated; warn loudly so operators
	// migrate. Only fires when an agent context exists. Kept ABOVE the
	// machine-global default because the selected agent's own LLM config
	// (provider/model/base_url) is more specific than a generic global
	// key — global auth still fills a credential gap here via
	// applyGlobalAuth.
	if agentDir != "" {
		if cfg, ok := readAgentFallback(agentDir, envLookup); ok {
			warning := "Skill builder is using the selected agent's LLM credentials. " +
				"This fallback is deprecated and will be removed in a future release. " +
				"Configure workspace-level skill-builder LLM under Settings → Skill Builder."
			out := resolve(cfg, SourceAgentFallback, warning, envLookup)
			applyGlobalAuth(&out, false, envLookup)
			markGateway(&out, workspaceDir)
			return out, nil
		}
	}

	// Tier 4: machine-global forge auth. Nothing else is configured, but
	// the operator may have a settings model gateway (enterprise Anthropic /
	// OpenAI), OAuthed (`forge init`/`forge try` OAuth flow ->
	// ~/.forge/credentials), or exported a global provider API key. Make
	// the skill/agent builder "just work" off that, same as `forge run`.
	// This is the primary path for the AI agent builder, which resolves
	// with no agent context (agentDir empty).
	if g, ok := resolveGlobalDefault(workspaceDir, envLookup); ok {
		return g, nil
	}

	return SkillBuilderLLM{Source: SourceUnset}, nil
}

// oauthCredChecker reports whether a usable machine-global OAuth token
// is stored for the given provider. It's a package var so tests can
// stub the credential store without touching the real ~/.forge files.
var oauthCredChecker = defaultOAuthCredChecker

// defaultOAuthCredChecker consults the real forge credential store.
// OAuth is only wired for provider=openai today — mirrors the agent
// runtime's createProviderClient, which gates the OAuth path on
// provider=="openai" and a stored token carrying a refresh token.
func defaultOAuthCredChecker(provider string) bool {
	if provider != "openai" {
		return false
	}
	tok, err := oauth.LoadCredentials(provider)
	return err == nil && tok != nil && tok.RefreshToken != ""
}

// applyGlobalAuth layers machine-global forge auth onto an already-
// resolved ui.yaml config. In the default (forceGlobal=false) mode it
// only fills a GAP — when the config resolved no usable API key. When
// forceGlobal is true (the config's use_global_auth toggle), global
// auth wins even over an api_key_env that did resolve.
//
// Resolution order for the credential, in both modes:
//  1. A stored OAuth token for the provider -> UseOAuth (token loaded
//     + refreshed by the caller at request time).
//  2. The provider's conventional global env var (OPENAI_API_KEY, …),
//     which covers the case where ui.yaml named a custom api_key_env
//     that happens to be empty while the standard global key is set.
func applyGlobalAuth(llm *SkillBuilderLLM, forceGlobal bool, envLookup func(string) string) {
	if llm.Provider == "" || llm.Provider == "ollama" {
		return
	}
	hasExplicit := llm.APIKey != "" && llm.APIKey != "__oauth__"
	if hasExplicit && !forceGlobal {
		return
	}

	if oauthCredChecker(llm.Provider) {
		llm.UseOAuth = true
		llm.APIKey = "" // token is loaded at request time, never cached here
		if llm.Source == SourceUnset || llm.Source == "" {
			llm.Source = SourceGlobalOAuth
		}
		return
	}

	if def := defaultAPIKeyEnv(llm.Provider); def != "" {
		if v := envLookup(def); v != "" {
			llm.APIKey = v
			llm.APIKeyEnv = def
			if llm.Source == SourceUnset || llm.Source == "" {
				llm.Source = SourceGlobalEnv
			}
		}
	}
}

// resolveGlobalDefault builds a SkillBuilderLLM purely from machine-
// global forge auth, used when no workspace/user ui.yaml exists. It
// prefers a stored OAuth token (openai) and otherwise picks the first
// provider whose conventional global API-key env var is set. The model
// defaults to a sensible per-provider choice the operator can override
// by writing a ui.yaml via Settings → Skill Builder.
func resolveGlobalDefault(workspaceDir string, envLookup func(string) string) (SkillBuilderLLM, bool) {
	// A settings model default (managed or user layer) whose provider has a
	// configured gateway is the managed operator's explicit intent — the
	// enterprise-Anthropic path. It wins over an incidental global key.
	if def := settingsModelDefault(workspaceDir); def != nil && def.Provider != "" {
		if gatewayChecker(workspaceDir, def.Provider) {
			model := def.Model
			if model == "" {
				model = defaultModelForProvider(def.Provider)
			}
			return SkillBuilderLLM{
				Provider:   def.Provider,
				Model:      model,
				Source:     SourceGlobalGateway,
				UseGateway: true,
			}, true
		}
	}
	if oauthCredChecker("openai") {
		return SkillBuilderLLM{
			Provider: "openai",
			Model:    defaultModelForProvider("openai"),
			Source:   SourceGlobalOAuth,
			UseOAuth: true,
		}, true
	}
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		env := defaultAPIKeyEnv(provider)
		if env == "" {
			continue
		}
		if v := envLookup(env); v != "" {
			return SkillBuilderLLM{
				Provider:  provider,
				Model:     defaultModelForProvider(provider),
				APIKeyEnv: env,
				APIKey:    v,
				Source:    SourceGlobalEnv,
			}, true
		}
	}
	return SkillBuilderLLM{}, false
}

// markGateway flags the resolved config when a model gateway is configured
// for its provider in the TRUSTED settings layers (system + user, never a
// cloned repo's project settings — same trust boundary the runtime and the
// builder's client construction enforce). When set, the builder
// authenticates through the gateway at request time even if no explicit key
// or OAuth token is present.
func markGateway(out *SkillBuilderLLM, workspaceDir string) {
	if out.Provider != "" && gatewayChecker(workspaceDir, out.Provider) {
		out.UseGateway = true
	}
}

// gatewayChecker reports whether a model gateway is configured for provider
// in the trusted settings layers under workspaceDir. Package var so tests can
// stub it without writing settings files.
var gatewayChecker = defaultGatewayChecker

func defaultGatewayChecker(workspaceDir, provider string) bool {
	if provider == "" {
		return false
	}
	layers, err := settings.LoadAllLayers(settings.LoadOptions{WorkingDir: workspaceDir})
	if err != nil {
		return false
	}
	return settings.Resolve(settings.TrustedGatewayLayers(layers)).Models.GatewayForProvider(provider) != nil
}

// settingsModelDefault returns the settings-resolved default provider/model
// (models.default), or nil when unset/unreadable. Package var for tests.
var settingsModelDefault = defaultSettingsModelDefault

func defaultSettingsModelDefault(workspaceDir string) *settings.ModelDefault {
	layers, err := settings.LoadAllLayers(settings.LoadOptions{WorkingDir: workspaceDir})
	if err != nil {
		return nil
	}
	return settings.Resolve(layers).Models.Default
}

// builderProviders is the ordered set of providers the build-time switch can
// offer. ollama is intentionally excluded — it's only usable when the operator
// has explicitly configured it as their provider (which surfaces as the
// resolved default), not as a machine-global credential the switch discovers.
var builderProviders = []string{"openai", "anthropic", "gemini"}

// candidateFor resolves the builder LLM for a specific provider using only the
// machine-global credential sources — a model gateway, a stored OAuth token,
// or a global API-key env var. Returns ok=false when the provider has no
// usable credential. The model comes from settings.models.default when it
// names this provider, else the per-provider default. Used to enumerate the
// providers the operator can switch between at build time.
func candidateFor(workspaceDir, provider string, envLookup func(string) string) (SkillBuilderLLM, bool) {
	if envLookup == nil {
		envLookup = os.Getenv
	}
	out := SkillBuilderLLM{Provider: provider, Model: defaultModelForProvider(provider)}
	if def := settingsModelDefault(workspaceDir); def != nil && def.Provider == provider && def.Model != "" {
		out.Model = def.Model
	}
	switch {
	case gatewayChecker(workspaceDir, provider):
		out.UseGateway = true
		out.Source = SourceGlobalGateway
		return out, true
	case provider == "openai" && oauthCredChecker("openai"):
		out.UseOAuth = true
		out.Source = SourceGlobalOAuth
		return out, true
	}
	if env := defaultAPIKeyEnv(provider); env != "" {
		if v := envLookup(env); v != "" {
			out.APIKey = v
			out.APIKeyEnv = env
			out.Source = SourceGlobalEnv
			return out, true
		}
	}
	return out, false
}

// AvailableSkillBuilderLLMs enumerates every builder LLM the operator can use
// right now — the resolved default first (LoadSkillBuilderLLM), then any other
// provider that has machine-global credentials (gateway / OAuth / global env).
// It backs the build-time provider switch: element 0 is the default the UI
// pre-selects; the rest are the alternatives the operator can flip to without
// opening Settings. Returns an empty slice when nothing is available.
func AvailableSkillBuilderLLMs(workspaceDir, agentDir string, envLookup func(string) string) ([]SkillBuilderLLM, error) {
	if envLookup == nil {
		envLookup = os.Getenv
	}
	def, err := LoadSkillBuilderLLM(workspaceDir, agentDir, envLookup)
	if err != nil {
		return nil, err
	}
	var out []SkillBuilderLLM
	seen := map[string]bool{}
	if def.Source != SourceUnset && def.HasCredentials() {
		out = append(out, def)
		seen[def.Provider] = true
	}
	for _, p := range builderProviders {
		if seen[p] {
			continue
		}
		if c, ok := candidateFor(workspaceDir, p, envLookup); ok {
			out = append(out, c)
			seen[p] = true
		}
	}
	return out, nil
}

// ResolveSkillBuilderLLMForProvider returns the available builder LLM for the
// named provider — the entry the build-time switch selected. An empty provider
// returns the default (element 0). ok=false when the requested provider isn't
// currently available (no credentials) or nothing is available at all, so the
// caller can 400 rather than stream an auth failure.
func ResolveSkillBuilderLLMForProvider(workspaceDir, agentDir, provider string, envLookup func(string) string) (SkillBuilderLLM, bool, error) {
	avail, err := AvailableSkillBuilderLLMs(workspaceDir, agentDir, envLookup)
	if err != nil {
		return SkillBuilderLLM{}, false, err
	}
	if len(avail) == 0 {
		return SkillBuilderLLM{Source: SourceUnset}, false, nil
	}
	if provider == "" {
		return avail[0], true, nil
	}
	for _, l := range avail {
		if l.Provider == provider {
			return l, true, nil
		}
	}
	return SkillBuilderLLM{}, false, nil
}

// defaultModelForProvider returns the default model for the global-auth
// convenience path (no ui.yaml). It reads forge-core/catalog — the single
// source of truth for provider/model metadata (#450/#461) — so it can never
// re-offer a Codex-retired model or drift from the wizard. catalog is pure
// data (no import cycle back into forge-ui). Empty for an unknown provider.
func defaultModelForProvider(provider string) string {
	if p, ok := catalog.ProviderByID(provider); ok {
		return p.DefaultModel
	}
	return ""
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
		Provider:      cfg.Provider,
		Model:         cfg.Model,
		BaseURL:       cfg.BaseURL,
		APIKeyEnv:     cfg.APIKeyEnv,
		Source:        source,
		Warning:       warning,
		UseGlobalAuth: cfg.UseGlobalAuth,
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
