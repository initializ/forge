package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-core/catalog"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/util"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/local"
)

// openAIProviderModels projects the OpenAI catalog entry into the wizard's
// ProviderModels shape. The catalog is the single source of truth for which
// models exist and which of them browser-based OAuth can actually reach.
func openAIProviderModels() ProviderModels {
	p, _ := catalog.ProviderByID("openai")
	toOptions := func(models []catalog.Model) []ModelOption {
		out := make([]ModelOption, 0, len(models))
		for _, m := range models {
			out = append(out, ModelOption{DisplayName: m.Label, ModelID: m.ModelID})
		}
		return out
	}
	return ProviderModels{
		Default:       p.DefaultModel,
		NeedsKey:      p.NeedsAPIKey,
		HasOAuth:      p.SupportsOAuth,
		SupportsOrgID: p.SupportsOrgID,
		APIKey:        toOptions(p.APIKeyModels()),
		OAuth:         toOptions(p.OAuthModels()),
	}
}

// handleGetWizardMeta returns all reference data the frontend wizard needs in a
// single call: providers, frameworks, channels, builtin tools, skills,
// per-provider model lists, and web search provider options.
func (s *UIServer) handleGetWizardMeta(w http.ResponseWriter, _ *http.Request) {
	meta := WizardMetadata{
		Providers:  []string{"openai", "anthropic", "bedrock", "gemini", "ollama", "custom"},
		Frameworks: []string{"forge", "crewai", "langchain"},
		Channels:   []string{"slack", "telegram"},
	}

	// Per-provider model lists
	meta.ProviderModels = map[string]ProviderModels{
		// OpenAI is projected from forge-core/catalog rather than duplicated
		// here. The two lists had drifted — this one still offered models the
		// Codex backend retired — which is exactly what the catalog exists to
		// prevent. APIKeyOnly in the catalog decides the OAuth/APIKey split.
		"openai": openAIProviderModels(),
		"anthropic": {
			Default:  "claude-sonnet-4-20250514",
			NeedsKey: true,
			APIKey: []ModelOption{
				{DisplayName: "Claude Sonnet 4", ModelID: "claude-sonnet-4-20250514"},
				{DisplayName: "Claude Haiku 3.5", ModelID: "claude-3-5-haiku-20241022"},
				{DisplayName: "Claude Opus 4", ModelID: "claude-opus-4-20250514"},
			},
		},
		"gemini": {
			Default:  "gemini-2.5-flash",
			NeedsKey: true,
			APIKey: []ModelOption{
				{DisplayName: "Gemini 2.5 Flash", ModelID: "gemini-2.5-flash"},
				{DisplayName: "Gemini 2.5 Pro", ModelID: "gemini-2.5-pro"},
			},
		},
		"ollama": {
			Default:  "llama3",
			NeedsKey: false,
			APIKey: []ModelOption{
				{DisplayName: "Llama 3", ModelID: "llama3"},
				{DisplayName: "Mistral", ModelID: "mistral"},
				{DisplayName: "CodeLlama", ModelID: "codellama"},
				{DisplayName: "Phi-3", ModelID: "phi3"},
			},
		},
		"custom": {
			Default:  "default",
			NeedsKey: true,
			IsCustom: true,
			// Custom-provider normalization (issue #83): the wizard's
			// Custom path is wired through provider=openai +
			// OPENAI_BASE_URL/OPENAI_API_KEY at scaffold time. The
			// frontend therefore writes OPENAI_BASE_URL directly
			// rather than the legacy MODEL_BASE_URL alias, which the
			// runtime resolver never read.
			BaseURLEnv: "OPENAI_BASE_URL",
		},
	}

	// Bedrock is sourced from the shared catalog (not hardcoded like the
	// other providers) so the web wizard's model list + region flag cannot
	// drift from the CLI/TUI. #205 review (provider-metadata-drift finding).
	if p, ok := catalog.ProviderByID("bedrock"); ok {
		bm := ProviderModels{
			Default:        p.DefaultModel,
			NeedsKey:       p.NeedsAPIKey,
			NeedsAWSRegion: p.NeedsAWSRegion,
		}
		for _, m := range p.Models {
			bm.APIKey = append(bm.APIKey, ModelOption{DisplayName: m.Label, ModelID: m.ModelID})
		}
		meta.ProviderModels["bedrock"] = bm
	}

	// Web search providers
	meta.WebSearchProviders = []WebSearchProviderOption{
		{
			Name:        "tavily",
			Label:       "Tavily (Recommended)",
			Description: "LLM-optimized search with structured results",
			EnvVar:      "TAVILY_API_KEY",
			Placeholder: "tvly-...",
		},
		{
			Name:        "perplexity",
			Label:       "Perplexity",
			Description: "AI-powered search with citations",
			EnvVar:      "PERPLEXITY_API_KEY",
			Placeholder: "pplx-...",
		},
	}

	// Builtin tools
	for _, t := range builtins.All() {
		meta.BuiltinTools = append(meta.BuiltinTools, BuiltinToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
		})
	}

	// Registry skills
	reg, err := local.NewEmbeddedRegistry()
	if err == nil {
		skills, listErr := reg.List()
		if listErr == nil {
			for _, sk := range skills {
				meta.Skills = append(meta.Skills, skillDescriptorToEntry(sk))
			}
		}
	}

	// Auth provider types — server-driven list. Adding a new provider
	// (e.g., Okta in Phase 3) means appending one entry here; the
	// frontend renders the picker from this metadata.
	meta.AuthProviderTypes = []AuthProviderTypeMeta{
		{Type: "none", Label: "None", Description: "Anonymous access — no auth: block written"},
		{Type: "oidc", Label: "OIDC (JWT)", Description: "Generic OIDC issuer (Keycloak, Auth0, Okta, Google …)"},
		{Type: "http_verifier", Label: "HTTP Verifier", Description: "Legacy — POST tokens to your own /verify endpoint"},
		{Type: "aws_sigv4", Label: "AWS Sigv4 (IAM)", Description: "Auth AWS-IAM callers via STS GetCallerIdentity (Phase 2)"},
		{Type: "gcp_iap", Label: "GCP Identity-Aware Proxy", Description: "Forge behind a GCP HTTPS LB+IAP (Phase 2)"},
		{Type: "azure_ad", Label: "Azure AD / Entra ID", Description: "Entra tenant tokens with optional Graph enrichment (Phase 2)"},
		{Type: "custom", Label: "Custom", Description: "Write a commented stub, edit forge.yaml manually"},
	}

	writeJSON(w, http.StatusOK, meta)
}

// handleCreateAgent creates a new agent via the injected CreateFunc.
func (s *UIServer) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CreateFunc == nil {
		writeError(w, http.StatusNotImplemented, "agent creation not available")
		return
	}

	var opts AgentCreateOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if opts.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if opts.ModelProvider == "" {
		writeError(w, http.StatusBadRequest, "model_provider is required")
		return
	}

	// Bedrock signs with SigV4 for a region-scoped endpoint, so the region
	// is required (drives host + signature scope). Reject early rather than
	// scaffold a forge.yaml that only fails at `forge validate`/run. #205.
	if opts.ModelProvider == "bedrock" && strings.TrimSpace(opts.AWSRegion) == "" {
		writeError(w, http.StatusBadRequest, "aws_region is required for model_provider \"bedrock\"")
		return
	}

	// Server-side validation of the auth payload before scaffolding the
	// agent on disk. Without this, a buggy frontend could write a
	// malformed auth: block into forge.yaml that only fails at
	// `forge run` — after the agent directory has been created and the
	// user thinks creation succeeded. Review #9.
	if err := validateAuthPayload(opts.Auth); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	agentDir, err := s.cfg.CreateFunc(opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	agentID := util.Slugify(opts.Name)

	// Broadcast creation event so the dashboard updates
	s.broker.Broadcast(SSEEvent{
		Type: "agent_created",
		Data: map[string]string{"id": agentID, "directory": agentDir},
	})

	writeJSON(w, http.StatusCreated, CreateAgentResponse{
		AgentID:   agentID,
		Directory: agentDir,
		Message:   "Agent created successfully",
	})
}

// validateAuthPayload runs the forge-core validate package over the
// wizard's auth payload before the agent is scaffolded on disk. Returns
// nil for benign shapes (absent, "none", "custom") and for valid
// provider configs; returns an error with the validation messages
// surfaced when settings are malformed.
//
// The translation from the wizard's AuthCreateOptions → types.AuthConfig
// mirrors exactly what cmd/ui.go's createFunc does later, so this check
// has the same view of the inputs as the eventual scaffold.
func validateAuthPayload(a *AuthCreateOptions) error {
	if a == nil {
		return nil
	}
	switch a.Mode {
	case "", "none", "custom":
		// Nothing to validate — these modes don't write a provider entry.
		return nil
	}

	// Filter the incoming Settings to the known-keys whitelist BEFORE
	// validation OR scaffolding. Closes the exploit chain (review M5):
	// without this, a POST with `{"settings": {"audience": "x",
	// "evil_key": "y"}}` would drop evil_key into forge.yaml verbatim.
	// Today provider Config structs ignore unknown YAML fields, but a
	// future field added without a `yaml:"-"` tag would suddenly become
	// reachable via untrusted POST. Filtering here gives defense-in-depth.
	a.Settings = validate.FilterKnownSettings(a.Mode, a.Settings)

	authYAML := types.AuthConfig{
		// Required is set true here because the wizard's renderAuthBlock
		// always emits required:true when a provider is chosen. Keeping
		// the validation view aligned with the rendered YAML avoids
		// drift between "wizard accepts" and "forge validate accepts".
		Required: true,
		Providers: []types.AuthProvider{
			{
				Type:     a.Mode,
				Settings: a.Settings,
			},
		},
	}

	result := &validate.ValidationResult{}
	validate.ValidateAuthConfig(authYAML, result)
	if !result.IsValid() {
		return fmt.Errorf("auth: %s", strings.Join(result.Errors, "; "))
	}
	return nil
}

// handleOAuthStart initiates the OAuth browser flow for a provider.
// The flow opens the user's browser for authentication and waits for the callback.
func (s *UIServer) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OAuthFunc == nil {
		writeError(w, http.StatusNotImplemented, "OAuth not available")
		return
	}

	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}

	token, err := s.cfg.OAuthFunc(req.Provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OAuth flow failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "success",
		"token":  token,
	})
}

// handleGetConfig returns the raw forge.yaml content for an agent.
func (s *UIServer) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	agent, ok := agents[id]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	configPath := filepath.Join(agent.Directory, "forge.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading config: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleUpdateConfig validates and saves forge.yaml for an agent.
func (s *UIServer) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	var req ConfigUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate YAML
	resp := validateConfigContent(req.Content)
	if !resp.Valid {
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}

	// Resolve agent directory
	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	agent, ok := agents[id]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	configPath := filepath.Join(agent.Directory, "forge.yaml")
	if err := os.WriteFile(configPath, []byte(req.Content), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "writing config: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleValidateConfig validates forge.yaml content without saving.
func (s *UIServer) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	var req ConfigUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp := validateConfigContent(req.Content)
	writeJSON(w, http.StatusOK, resp)
}

// handleListSkills returns all registry skills, optionally filtered by category.
func (s *UIServer) handleListSkills(w http.ResponseWriter, r *http.Request) {
	reg, err := local.NewEmbeddedRegistry()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading skill registry: "+err.Error())
		return
	}

	skills, err := reg.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing skills: "+err.Error())
		return
	}

	categoryFilter := r.URL.Query().Get("category")
	var entries []SkillBrowserEntry
	for _, sk := range skills {
		if categoryFilter != "" && !strings.EqualFold(sk.Category, categoryFilter) {
			continue
		}
		entries = append(entries, skillDescriptorToEntry(sk))
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	writeJSON(w, http.StatusOK, entries)
}

// handleGetSkillContent returns the raw SKILL.md content for a skill.
func (s *UIServer) handleGetSkillContent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "skill name is required")
		return
	}

	reg, err := local.NewEmbeddedRegistry()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading skill registry: "+err.Error())
		return
	}

	content, err := reg.LoadContent(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "skill not found: "+name)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// handleListBuiltinTools returns all builtin tools with descriptions.
func (s *UIServer) handleListBuiltinTools(w http.ResponseWriter, _ *http.Request) {
	var tools []BuiltinToolInfo
	for _, t := range builtins.All() {
		tools = append(tools, BuiltinToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
		})
	}
	writeJSON(w, http.StatusOK, tools)
}

// validateConfigContent parses and validates forge.yaml content.
func validateConfigContent(content string) ConfigValidateResponse {
	cfg, err := types.ParseForgeConfig([]byte(content))
	if err != nil {
		return ConfigValidateResponse{
			Valid:  false,
			Errors: []string{err.Error()},
		}
	}

	result := validate.ValidateForgeConfig(cfg)
	return ConfigValidateResponse{
		Valid:    result.IsValid(),
		Errors:   result.Errors,
		Warnings: result.Warnings,
	}
}

// skillDescriptorToEntry converts a contract.SkillDescriptor to a SkillBrowserEntry.
func skillDescriptorToEntry(sk contract.SkillDescriptor) SkillBrowserEntry {
	return SkillBrowserEntry{
		Name:          sk.Name,
		DisplayName:   sk.DisplayName,
		Description:   sk.Description,
		Category:      sk.Category,
		Tags:          sk.Tags,
		RequiredEnv:   sk.RequiredEnv,
		OneOfEnv:      sk.OneOfEnv,
		OptionalEnv:   sk.OptionalEnv,
		RequiredBins:  sk.RequiredBins,
		EgressDomains: sk.EgressDomains,
	}
}
