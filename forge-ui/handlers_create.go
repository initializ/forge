package forgeui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/util"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/local"
)

// handleGetWizardMeta returns all reference data the frontend wizard needs in a
// single call: providers, frameworks, channels, builtin tools, skills,
// per-provider model lists, and web search provider options.
func (s *UIServer) handleGetWizardMeta(w http.ResponseWriter, _ *http.Request) {
	meta := WizardMetadata{
		Providers:  []string{"openai", "anthropic", "gemini", "ollama", "custom"},
		Frameworks: []string{"forge", "crewai", "langchain"},
		Channels:   []string{"slack", "telegram"},
	}

	// Per-provider model lists
	meta.ProviderModels = map[string]ProviderModels{
		"openai": {
			Default:  "gpt-5.2-2025-12-11",
			NeedsKey: true,
			HasOAuth: true,
			APIKey: []ModelOption{
				{DisplayName: "GPT 5.2", ModelID: "gpt-5.2-2025-12-11"},
				{DisplayName: "GPT 5 Mini", ModelID: "gpt-5-mini-2025-08-07"},
				{DisplayName: "GPT 5 Nano", ModelID: "gpt-5-nano-2025-08-07"},
				{DisplayName: "GPT 4.1 Mini", ModelID: "gpt-4.1-mini-2025-04-14"},
			},
			OAuth: []ModelOption{
				{DisplayName: "GPT 5.3 Codex", ModelID: "gpt-5.3-codex"},
				{DisplayName: "GPT 5.2", ModelID: "gpt-5.2-2025-12-11"},
				{DisplayName: "GPT 5.2 Codex", ModelID: "gpt-5.2-codex"},
			},
		},
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
			Default:    "default",
			NeedsKey:   true,
			IsCustom:   true,
			BaseURLEnv: "MODEL_BASE_URL",
		},
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
