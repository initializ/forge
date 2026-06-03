package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/initializ/forge/forge-ui/uiconfig"
)

// SkillBuilderCodegenModel previously hardcoded "gpt-4.1" / "claude-opus-4-6"
// regardless of the agent's configured model. Issue #92 removed that override:
// the skill builder now uses the operator-chosen model from workspace-level
// ui.yaml (see uiconfig.LoadSkillBuilderLLM). The function is retained as a
// no-op shim with a deprecation marker so any out-of-tree callers fail loudly.
//
// Deprecated: skill-builder model selection is now driven by uiconfig.
func SkillBuilderCodegenModel(_, configured string) string {
	return configured
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

// handleSkillBuilderProvider reports the resolved skill-builder LLM
// configuration. Workspace-level config (forge-ui/uiconfig) is the
// primary source; the agent's forge.yaml is consulted only when no
// workspace/user config exists (deprecated fallback). The handler
// never mutates the UI process's environment.
func (s *UIServer) handleSkillBuilderProvider(w http.ResponseWriter, r *http.Request) {
	// agentDir is only used for the deprecated fallback path. It's
	// optional — first-run flow (no agent picked yet) is supported.
	agentDir := ""
	if r.PathValue("id") != "" {
		agentDir = s.resolveAgentDir(w, r)
		if agentDir == "" {
			return // resolveAgentDir wrote the error response
		}
	}

	llm, err := uiconfig.LoadSkillBuilderLLM(s.cfg.WorkDir, agentDir, uiconfig.EnvLookupForWorkspace(s.cfg.WorkDir))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading skill-builder config: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider": llm.Provider,
		"model":    llm.Model,
		"base_url": llm.BaseURL,
		"has_key":  llm.HasCredentials(),
		"source":   llm.Source,
		"warning":  llm.Warning,
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

	// Resolve the workspace-level skill-builder LLM ONCE per request and
	// pass it through LLMStreamOptions. The forge-cli callback consumes
	// LLM directly — it must not re-read the agent's forge.yaml / .env,
	// since that would re-introduce the per-agent env-stomping the
	// workspace-LLM design replaced (issue #92).
	llm, err := uiconfig.LoadSkillBuilderLLM(s.cfg.WorkDir, agentDir, uiconfig.EnvLookupForWorkspace(s.cfg.WorkDir))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading skill-builder config: "+err.Error())
		return
	}
	if llm.Source == uiconfig.SourceUnset {
		writeError(w, http.StatusBadRequest,
			"skill-builder LLM is not configured. Open Settings → Skill Builder to pick a provider, model, and API key env var.")
		return
	}
	if !llm.HasCredentials() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"skill-builder LLM is configured (%s) but no API key found in env var %q. "+
				"Set that env var in the forge ui process and reload, or change api_key_env in Settings.",
			llm.Provider, llm.APIKeyEnv))
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

	err = s.cfg.LLMStreamFunc(r.Context(), LLMStreamOptions{
		LLM:          llm,
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
