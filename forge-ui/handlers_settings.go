package forgeui

import (
	"encoding/json"
	"net/http"

	"github.com/initializ/forge/forge-ui/uiconfig"
)

// skillBuilderSettingsRequest is the PUT body shape. The api_key field
// is INTENTIONALLY not part of uiconfig.SkillBuilderConfig — that
// struct is the on-disk YAML shape and the key value must never be
// persisted there. Instead, when api_key is present the handler writes
// it to <workspace>/.forge/.env under the api_key_env name. Keeping
// the two flows in one PUT means the UI submits one form and the
// operator doesn't have to coordinate two endpoints.
type skillBuilderSettingsRequest struct {
	uiconfig.SkillBuilderConfig
	APIKey string `json:"api_key,omitempty"`
}

// handleGetSkillBuilderSettings returns the current workspace-level
// skill-builder configuration plus enough metadata for the Settings
// page UI to render its form (current source, available providers,
// detected env var presence).
func (s *UIServer) handleGetSkillBuilderSettings(w http.ResponseWriter, _ *http.Request) {
	// AgentDir is empty here — Settings is workspace-level. The loader
	// will return Source=unset if no workspace/user config exists.
	llm, err := uiconfig.LoadSkillBuilderLLM(s.cfg.WorkDir, "", uiconfig.EnvLookupForWorkspace(s.cfg.WorkDir))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading settings: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider":    llm.Provider,
		"model":       llm.Model,
		"base_url":    llm.BaseURL,
		"api_key_env": llm.APIKeyEnv,
		"has_key":     llm.HasCredentials(),
		"source":      llm.Source,
		"warning":     llm.Warning,
		"providers":   []string{"openai", "anthropic", "gemini", "ollama"},
	})
}

// handlePutSkillBuilderSettings validates the submitted config and
// persists it to <workspace>/.forge/ui.yaml. When the request carries
// an api_key value, it is written to <workspace>/.forge/.env under
// the api_key_env name (or the provider default) with 0600
// permissions and a sibling .gitignore protecting it.
func (s *UIServer) handlePutSkillBuilderSettings(w http.ResponseWriter, r *http.Request) {
	var body skillBuilderSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := uiconfig.ValidateSkillBuilderConfig(body.SkillBuilderConfig); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := uiconfig.SaveSkillBuilderLLM(s.cfg.WorkDir, body.SkillBuilderConfig); err != nil {
		writeError(w, http.StatusInternalServerError, "saving settings: "+err.Error())
		return
	}

	// Persist the API key to <workspace>/.forge/.env when provided.
	// The key NAME is taken from body.APIKeyEnv if set, otherwise the
	// provider default — this matches what the loader will look up at
	// request time.
	if body.APIKey != "" {
		envName := body.APIKeyEnv
		if envName == "" {
			envName = defaultAPIKeyEnv(body.Provider)
		}
		if envName == "" {
			writeError(w, http.StatusBadRequest,
				"cannot persist api_key for this provider (no default env var name); set api_key_env explicitly")
			return
		}
		if err := uiconfig.SetEnvFileValue(s.cfg.WorkDir, envName, body.APIKey); err != nil {
			writeError(w, http.StatusInternalServerError, "saving api key: "+err.Error())
			return
		}
	}

	// Echo back the resolved state so the UI can update its banner
	// without a second round trip.
	llm, err := uiconfig.LoadSkillBuilderLLM(s.cfg.WorkDir, "", uiconfig.EnvLookupForWorkspace(s.cfg.WorkDir))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reloading settings: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":    llm.Provider,
		"model":       llm.Model,
		"base_url":    llm.BaseURL,
		"api_key_env": llm.APIKeyEnv,
		"has_key":     llm.HasCredentials(),
		"source":      llm.Source,
		"warning":     llm.Warning,
	})
}

// defaultAPIKeyEnv mirrors uiconfig's internal mapping so the settings
// handler knows where to persist the api_key when the operator didn't
// override APIKeyEnv. Keeping a copy here (rather than exporting from
// uiconfig) keeps the small list visible at the use site; if it grows
// we can promote it.
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
