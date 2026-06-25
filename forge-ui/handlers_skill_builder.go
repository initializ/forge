package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-skills/parser"
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
//
// Returns the create-mode prompt by default. Edit-mode prompts are
// constructed on-demand by handleSkillBuilderChat itself (which loads
// the live on-disk state) so this endpoint stays cheap and stateless —
// it's only used by the UI to surface the prompt for the operator to
// read, never to drive an actual LLM call.
func (s *UIServer) handleSkillBuilderContext(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"system_prompt": skillBuilderSystemPrompt(modeCreate, nil),
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

	// Build the system prompt. In edit mode the handler loads the on-
	// disk SKILL.md + scripts itself (single source of truth — never
	// trust UI-provided content here, since a stale or tampered editor
	// state would feed the LLM the wrong baseline) and primes the
	// prompt with an "## Edit Mode" trailer. See issue #193.
	mode := modeCreate
	var existing *existingSkillContext
	if req.Mode == "edit" {
		if err := validateSkillNameForPath(req.EditingName); err != nil {
			writeError(w, http.StatusBadRequest, "editing_name: "+err.Error())
			return
		}
		ex, err := readCustomSkill(agentDir, req.EditingName)
		if err != nil {
			writeError(w, http.StatusNotFound, "loading skill to edit: "+err.Error())
			return
		}
		mode = modeEdit
		existing = &existingSkillContext{
			Name:    ex.Name,
			SkillMD: ex.SkillMD,
			Scripts: ex.Scripts,
		}
	}
	systemPrompt := skillBuilderSystemPrompt(mode, existing)

	var fullResponse strings.Builder

	err = s.cfg.LLMStreamFunc(r.Context(), LLMStreamOptions{
		LLM:          llm,
		AgentDir:     agentDir,
		SystemPrompt: systemPrompt,
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

// handleSkillBuilderListCustomSkills lists project-local skills attached
// to the agent (issue #193). Builtin / embedded registry skills are NOT
// included — those live in the binary, can't be edited from the
// dashboard, and have their own /api/skills endpoint.
func (s *UIServer) handleSkillBuilderListCustomSkills(w http.ResponseWriter, r *http.Request) {
	agentDir := s.resolveAgentDir(w, r)
	if agentDir == "" {
		return
	}
	skills, err := listCustomSkills(agentDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing custom skills: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// handleSkillBuilderGetCustomSkill returns the SKILL.md and scripts for
// a single custom skill so the Skill Builder can populate the editor in
// edit mode (issue #193). 404 if the skill doesn't exist on disk; 400
// for an invalid name.
func (s *UIServer) handleSkillBuilderGetCustomSkill(w http.ResponseWriter, r *http.Request) {
	agentDir := s.resolveAgentDir(w, r)
	if agentDir == "" {
		return
	}
	name := r.PathValue("name")
	if err := validateSkillNameForPath(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	content, err := readCustomSkill(agentDir, name)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "loading skill: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, content)
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

	// In edit mode, suppress the duplicate-name warning for the skill
	// being edited; a rename still warns. See validateSkillMD (issue #193).
	editing := ""
	if req.Mode == "edit" {
		editing = req.EditingName
	}
	result := validateSkillMD(req.SkillMD, req.Scripts, agentDir, editing)
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

	if err := validateSkillNameForPath(req.SkillName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Edit-mode safety: overwrite is only valid when the request's
	// editing_name matches the skill_name being saved. Trying to
	// overwrite a DIFFERENT skill's directory is rejected here so the
	// SkillSaveFunc never sees a request that would clobber an
	// unrelated skill (issue #193).
	if req.Overwrite && req.EditingName != req.SkillName {
		writeError(w, http.StatusBadRequest,
			"overwrite is only allowed when editing_name matches skill_name")
		return
	}
	if req.EditingName != "" {
		if err := validateSkillNameForPath(req.EditingName); err != nil {
			writeError(w, http.StatusBadRequest, "editing_name: "+err.Error())
			return
		}
	}

	// Validate content first. In edit mode the duplicate-name warning
	// is suppressed for the skill being edited — see validateSkillMD.
	editingForValidate := ""
	if req.Overwrite {
		editingForValidate = req.EditingName
	}
	result := validateSkillMD(req.SkillMD, req.Scripts, agentDir, editingForValidate)
	if !result.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":      "validation failed",
			"validation": result,
		})
		return
	}

	saveResult, err := s.cfg.SkillSaveFunc(SkillSaveOptions{
		AgentDir:    agentDir,
		SkillName:   req.SkillName,
		SkillMD:     req.SkillMD,
		Scripts:     req.Scripts,
		EnvVars:     req.EnvVars,
		Overwrite:   req.Overwrite,
		EditingName: req.EditingName,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "saving skill: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, saveResult)
}

// validateSkillNameForPath checks that a skill name is safe to use as
// a path component. Shared between the save, load, and edit-mode chat
// handlers — skill names always cross the filesystem boundary so the
// guard MUST be identical across every entry point.
func validateSkillNameForPath(name string) error {
	if name == "" {
		return fmt.Errorf("skill_name is required")
	}
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("skill_name must be lowercase kebab-case")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("skill_name contains invalid characters")
	}
	return nil
}

// listCustomSkillFiles globs the agent's skills directory for SKILL.md
// files — both the subdir form (skills/<name>/SKILL.md) and the flat
// form (skills/<name>.md). The agent's root SKILL.md is deliberately
// excluded: it describes the agent itself, not an attached skill.
//
// Returned paths are absolute. Used by the list-custom-skills endpoint
// (issue #193) and only here — the runtime has its own equivalent walk
// at forge-cli/runtime/runner.go:discoverSkillFiles which we don't
// import to avoid a forge-cli → forge-ui dependency cycle.
func listCustomSkillFiles(agentDir string) ([]string, error) {
	skillsDir := filepath.Join(agentDir, "skills")
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			subdirSkill := filepath.Join(skillsDir, e.Name(), "SKILL.md")
			if _, err := os.Stat(subdirSkill); err == nil {
				paths = append(paths, subdirSkill)
			}
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") && e.Name() != "SKILL.md" {
			paths = append(paths, filepath.Join(skillsDir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// listCustomSkills walks the agent's skills/ directory and returns one
// CustomSkillSummary per discovered SKILL.md (issue #193). A skill with
// a parse error is logged-and-skipped rather than failing the whole
// list — the operator may have a half-edited skill on disk and we
// want the rest of the picker to keep working.
func listCustomSkills(agentDir string) ([]CustomSkillSummary, error) {
	paths, err := listCustomSkillFiles(agentDir)
	if err != nil {
		return nil, err
	}
	summaries := make([]CustomSkillSummary, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		entries, meta, err := parser.ParseWithMetadata(f)
		_ = f.Close()
		if err != nil || meta == nil {
			continue
		}
		// Derive the directory-name for path-style identification.
		// For the subdir form, the skill's directory IS the canonical
		// identifier; for flat form, it's the file's basename minus
		// .md. The frontmatter Name takes precedence either way (it's
		// what `forge skills validate` and the runtime register on),
		// but we fall back to the directory name when frontmatter
		// Name is empty so a half-authored skill still appears in
		// the list.
		display := meta.Name
		if display == "" {
			dir := filepath.Dir(p)
			if filepath.Base(p) == "SKILL.md" {
				display = filepath.Base(dir)
			} else {
				display = strings.TrimSuffix(filepath.Base(p), ".md")
			}
		}
		rel, _ := filepath.Rel(agentDir, p)
		if rel == "" {
			rel = p
		}
		hasScripts := false
		if filepath.Base(p) == "SKILL.md" {
			scriptsDir := filepath.Join(filepath.Dir(p), "scripts")
			if info, statErr := os.Stat(scriptsDir); statErr == nil && info.IsDir() {
				hasScripts = true
			}
		}
		var toolNames []string
		for _, e := range entries {
			if e.Name != "" {
				toolNames = append(toolNames, e.Name)
			}
		}
		summaries = append(summaries, CustomSkillSummary{
			Name:        display,
			Description: meta.Description,
			Category:    meta.Category,
			Tags:        meta.Tags,
			Path:        rel,
			HasScripts:  hasScripts,
			Tools:       toolNames,
		})
	}
	return summaries, nil
}

// readCustomSkill loads a skill's SKILL.md and any helper scripts into
// memory so the Skill Builder can populate the editor (issue #193).
// Tries subdir form (skills/<name>/SKILL.md) first, then flat form
// (skills/<name>.md). Returns an os.IsNotExist-compatible error when
// neither path exists so the handler can return 404.
//
// Symlink hardening: scripts are walked with strict symlink-escape
// rejection. Any script whose resolved target falls outside the
// skill's own directory is dropped from the result — a malicious or
// accidental link to /etc/passwd never reaches the editor.
func readCustomSkill(agentDir, name string) (*CustomSkillContent, error) {
	subdirPath := filepath.Join(agentDir, "skills", name, "SKILL.md")
	flatPath := filepath.Join(agentDir, "skills", name+".md")

	var path, format string
	if _, err := os.Stat(subdirPath); err == nil {
		path = subdirPath
		format = "subdir"
	} else if _, err := os.Stat(flatPath); err == nil {
		path = flatPath
		format = "flat"
	} else {
		return nil, os.ErrNotExist
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(agentDir, path)
	if rel == "" {
		rel = path
	}

	content := &CustomSkillContent{
		Name:    name,
		SkillMD: string(data),
		Path:    rel,
		Format:  format,
	}

	if format == "subdir" {
		scriptsDir := filepath.Join(agentDir, "skills", name, "scripts")
		scripts, err := readSkillScripts(scriptsDir)
		if err != nil {
			return nil, err
		}
		content.Scripts = scripts
	}
	return content, nil
}

// readSkillScripts loads every regular file under scriptsDir into a
// basename → content map. Symlinks whose resolved target escapes
// scriptsDir are skipped (defense in depth — the skill directory is
// trusted, but a symlink inside it isn't).
func readSkillScripts(scriptsDir string) (map[string]string, error) {
	info, err := os.Stat(scriptsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	scriptsAbs, err := filepath.Abs(scriptsDir)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(scriptsAbs); err == nil {
		scriptsAbs = real
	}

	scripts := make(map[string]string)
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		entryPath := filepath.Join(scriptsDir, e.Name())
		realPath, err := filepath.EvalSymlinks(entryPath)
		if err != nil {
			continue
		}
		// Reject if the resolved target falls outside scriptsAbs —
		// stops a symlink-to-/etc/passwd from leaking the file into
		// the editor and from there into the LLM context.
		realAbs, err := filepath.Abs(realPath)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(realAbs+string(os.PathSeparator), scriptsAbs+string(os.PathSeparator)) &&
			realAbs != scriptsAbs {
			continue
		}
		data, err := os.ReadFile(entryPath)
		if err != nil {
			continue
		}
		scripts[e.Name()] = string(data)
	}
	return scripts, nil
}
