package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/types"
)

// SkillBuilderCodegenModel returns the preferred code-generation model for the
// given provider. Skill generation is a complex task that benefits from stronger
// models than the agent's default. Falls back to fallback if the provider is unknown.
func SkillBuilderCodegenModel(provider, fallback string) string {
	switch provider {
	case "openai":
		return "gpt-4.1"
	case "anthropic":
		return "claude-opus-4-6"
	default:
		return fallback
	}
}

// resolveAgentDir extracts agent ID from the request, looks up the agent,
// and returns its directory. Writes an error response and returns "" on failure.
func (s *UIServer) resolveAgentDir(w http.ResponseWriter, r *http.Request) string {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return ""
	}

	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return ""
	}

	agent, ok := agents[id]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return ""
	}

	return agent.Directory
}

// handleSkillBuilderProvider returns the agent's LLM provider info.
func (s *UIServer) handleSkillBuilderProvider(w http.ResponseWriter, r *http.Request) {
	agentDir := s.resolveAgentDir(w, r)
	if agentDir == "" {
		return
	}

	configPath := filepath.Join(agentDir, "forge.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading config: "+err.Error())
		return
	}

	cfg, err := types.ParseForgeConfig(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "parsing config: "+err.Error())
		return
	}

	provider := cfg.Model.Provider

	// Load the agent's .env and encrypted secrets so we can check for API keys
	// that aren't in the UI process's own environment.
	envPath := filepath.Join(agentDir, ".env")
	envVars, _ := runtime.LoadEnvFile(envPath)
	for k, v := range envVars {
		// Don't pollute process env with __oauth__ sentinels — they block
		// OverlaySecretsToEnv from replacing them with real keys later.
		if v == "__oauth__" {
			continue
		}
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	runtime.OverlaySecretsToEnv(cfg, agentDir)

	// Check if the provider's API key env var is set (excluding __oauth__ sentinel)
	keyVal := func(k string) bool {
		v := os.Getenv(k)
		return v != "" && v != "__oauth__"
	}
	hasKey := false
	isOAuth := false
	switch provider {
	case "openai":
		hasKey = keyVal("OPENAI_API_KEY")
		if !hasKey {
			// Check for stored OAuth credentials
			if token, err := oauth.LoadCredentials("openai"); err == nil && token != nil && token.RefreshToken != "" {
				hasKey = true
				isOAuth = true
			}
		}
	case "anthropic":
		hasKey = keyVal("ANTHROPIC_API_KEY")
	case "gemini":
		hasKey = keyVal("GEMINI_API_KEY")
	case "ollama":
		hasKey = true // Ollama doesn't need an API key
	default:
		hasKey = keyVal("LLM_API_KEY") || keyVal("MODEL_API_KEY")
	}

	// OAuth/Codex backend has model restrictions — use agent's configured model.
	// API key clients get upgraded to a stronger codegen model.
	model := cfg.Model.Name
	if !isOAuth {
		model = SkillBuilderCodegenModel(provider, model)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider": provider,
		"model":    model,
		"has_key":  hasKey,
	})
}

// handleSkillBuilderContext returns the system prompt for the skill builder.
func (s *UIServer) handleSkillBuilderContext(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"system_prompt": skillBuilderSystemPrompt,
	})
}

// handleSkillBuilderChat streams an LLM conversation for skill building via SSE.
func (s *UIServer) handleSkillBuilderChat(w http.ResponseWriter, r *http.Request) {
	if s.cfg.LLMStreamFunc == nil {
		writeError(w, http.StatusNotImplemented, "skill builder LLM streaming not available")
		return
	}

	agentDir := s.resolveAgentDir(w, r)
	if agentDir == "" {
		return
	}

	var req SkillBuilderChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages are required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	var fullResponse strings.Builder

	err := s.cfg.LLMStreamFunc(r.Context(), LLMStreamOptions{
		AgentDir:     agentDir,
		SystemPrompt: skillBuilderSystemPrompt,
		Messages:     req.Messages,
		OnChunk: func(chunk string) {
			fullResponse.WriteString(chunk)
			data, _ := json.Marshal(map[string]string{"content": chunk})
			_, _ = fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", data)
			flusher.Flush()
		},
		OnDone: func(response string) {
			// Extract artifacts from the full response
			skillMD, scripts := extractArtifacts(response)
			if skillMD != "" {
				draftData, _ := json.Marshal(map[string]any{
					"skill_md": skillMD,
					"scripts":  scripts,
				})
				_, _ = fmt.Fprintf(w, "event: skill_draft\ndata: %s\n\n", draftData)
				flusher.Flush()
			}

			doneData, _ := json.Marshal(map[string]string{"status": "complete"})
			_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneData)
			flusher.Flush()
		},
	})

	if err != nil {
		errData, _ := json.Marshal(map[string]string{"error": err.Error()})
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", errData)
		flusher.Flush()
	}
}

// handleSkillBuilderValidate validates a SKILL.md and optional scripts.
func (s *UIServer) handleSkillBuilderValidate(w http.ResponseWriter, r *http.Request) {
	agentDir := s.resolveAgentDir(w, r)
	if agentDir == "" {
		return
	}

	var req SkillBuilderValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result := validateSkillMD(req.SkillMD, req.Scripts, agentDir)
	writeJSON(w, http.StatusOK, result)
}

// handleSkillBuilderSave saves a validated skill to the agent's skills directory.
func (s *UIServer) handleSkillBuilderSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SkillSaveFunc == nil {
		writeError(w, http.StatusNotImplemented, "skill saving not available")
		return
	}

	agentDir := s.resolveAgentDir(w, r)
	if agentDir == "" {
		return
	}

	var req SkillBuilderSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate skill name format (security: prevent path traversal)
	if req.SkillName == "" {
		writeError(w, http.StatusBadRequest, "skill_name is required")
		return
	}
	if !skillNamePattern.MatchString(req.SkillName) {
		writeError(w, http.StatusBadRequest, "skill_name must be lowercase kebab-case")
		return
	}
	if strings.Contains(req.SkillName, "/") || strings.Contains(req.SkillName, "\\") || strings.Contains(req.SkillName, "..") {
		writeError(w, http.StatusBadRequest, "skill_name contains invalid characters")
		return
	}

	// Validate content first
	result := validateSkillMD(req.SkillMD, req.Scripts, agentDir)
	if !result.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":      "validation failed",
			"validation": result,
		})
		return
	}

	saveResult, err := s.cfg.SkillSaveFunc(SkillSaveOptions{
		AgentDir:  agentDir,
		SkillName: req.SkillName,
		SkillMD:   req.SkillMD,
		Scripts:   req.Scripts,
		EnvVars:   req.EnvVars,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "saving skill: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, saveResult)
}
