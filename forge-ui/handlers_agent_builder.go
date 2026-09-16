package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-skills/local"
	"github.com/initializ/forge/forge-ui/uiconfig"
)

// handleAgentBuilderChat streams an LLM conversation that designs a whole
// agent (the conversational alternative to the New Agent wizard) via SSE.
//
// It mirrors handleSkillBuilderChat but is workspace-level: no agent
// exists yet, so it resolves the skill-builder LLM with an empty
// agentDir (workspace/user ui.yaml or machine-global auth — issue #92
// follow-up). The response is a structured {message, agent} envelope; the
// handler emits `message` and `agent_draft` SSE events, never raw JSON
// tokens, so the chat bubble never shows a half-formed object.
func (s *UIServer) handleAgentBuilderChat(w http.ResponseWriter, r *http.Request) {
	if s.cfg.LLMStreamFunc == nil {
		writeError(w, http.StatusNotImplemented, "agent builder LLM streaming not available")
		return
	}

	var req AgentBuilderChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages are required")
		return
	}

	// Workspace-level resolution — agentDir is empty. req.Provider carries
	// the build-time provider switch selection (empty = resolved default).
	// Same credential gate as the skill builder: refuse early with an
	// actionable message rather than stream an auth failure.
	llm, ok, err := uiconfig.ResolveSkillBuilderLLMForProvider(s.cfg.WorkDir, "", req.Provider, uiconfig.EnvLookupForWorkspace(s.cfg.WorkDir))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading builder config: "+err.Error())
		return
	}
	if !ok || llm.Source == uiconfig.SourceUnset {
		if req.Provider != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"provider %q is not available for the AI builder (no gateway, OAuth, or API key). Pick an available provider or configure one.", req.Provider))
			return
		}
		writeError(w, http.StatusBadRequest,
			"AI builder LLM is not configured. OAuth or set an API key at the machine-global level, or pick a provider/model under Settings → Skill Builder.")
		return
	}
	if !llm.HasCredentials() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"AI builder LLM is configured (%s) but no credentials were found. "+
				"OAuth globally, set %q in the forge ui process, or change it under Settings → Skill Builder.",
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

	systemPrompt := agentBuilderSystemPrompt(defaultProviderModels(), s.builtinToolInfos(), s.registrySkillEntries())

	var lastProgress time.Time
	err = s.cfg.LLMStreamFunc(r.Context(), LLMStreamOptions{
		LLM:          llm,
		AgentDir:     "",
		SystemPrompt: systemPrompt,
		Messages:     req.Messages,
		OnChunk: func(chunk string) {
			// Keepalive ping only — the response is a structured JSON
			// envelope delivered at completion, so we never forward raw
			// tokens (same rationale as the skill builder). Throttle to
			// ~2/sec.
			if now := time.Now(); now.Sub(lastProgress) >= 500*time.Millisecond {
				lastProgress = now
				_, _ = fmt.Fprint(w, "event: progress\ndata: {}\n\n")
				flusher.Flush()
			}
		},
		OnDone: func(response string) {
			message, agent, _ := parseAgentEnvelope(response)

			if message != "" {
				msgData, _ := json.Marshal(map[string]string{"content": message})
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", msgData)
				flusher.Flush()
			}
			if agent != nil {
				draftData, _ := json.Marshal(agent)
				_, _ = fmt.Fprintf(w, "event: agent_draft\ndata: %s\n\n", draftData)
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

// builtinToolInfos returns the builtin tool catalog the agent builder
// offers the LLM — the same list the wizard-meta handler surfaces.
func (s *UIServer) builtinToolInfos() []BuiltinToolInfo {
	var tools []BuiltinToolInfo
	for _, t := range builtins.All() {
		tools = append(tools, BuiltinToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
		})
	}
	return tools
}

// registrySkillEntries returns the embedded registry skills the agent
// builder can attach. A registry load failure is non-fatal — the builder
// just offers no skills rather than failing the whole conversation.
func (s *UIServer) registrySkillEntries() []SkillBrowserEntry {
	reg, err := local.NewEmbeddedRegistry()
	if err != nil {
		return nil
	}
	skills, err := reg.List()
	if err != nil {
		return nil
	}
	entries := make([]SkillBrowserEntry, 0, len(skills))
	for _, sk := range skills {
		entries = append(entries, skillDescriptorToEntry(sk))
	}
	return entries
}
