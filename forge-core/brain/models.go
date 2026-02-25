package brain

// ModelInfo describes a GGUF model available for local inference.
type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Default  bool   `json:"default"`
}

// modelRegistry is the embedded catalog of supported brain models.
var modelRegistry = []ModelInfo{
	{
		ID:       "qwen3-0.6b-q4km",
		Name:     "Qwen3 0.6B",
		Filename: "Qwen3-0.6B-Q4_K_M.gguf",
		URL:      "https://huggingface.co/unsloth/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q4_K_M.gguf",
		SHA256:   "", // populated after first verified download
		Size:     397_000_000,
		Default:  true,
	},
	{
		ID:       "phi4-mini-q4km",
		Name:     "Phi-4 Mini",
		Filename: "Phi-4-mini-instruct-Q4_K_M.gguf",
		URL:      "https://huggingface.co/unsloth/Phi-4-mini-instruct-GGUF/resolve/main/Phi-4-mini-instruct-Q4_K_M.gguf",
		SHA256:   "",
		Size:     2_490_000_000,
		Default:  false,
	},
}

// ListModels returns a copy of the model registry.
func ListModels() []ModelInfo {
	out := make([]ModelInfo, len(modelRegistry))
	copy(out, modelRegistry)
	return out
}

// DefaultModel returns the default model from the registry.
func DefaultModel() ModelInfo {
	for _, m := range modelRegistry {
		if m.Default {
			return m
		}
	}
	return modelRegistry[0]
}

// LookupModel finds a model by ID. Returns the model and true if found.
func LookupModel(id string) (ModelInfo, bool) {
	for _, m := range modelRegistry {
		if m.ID == id {
			return m, true
		}
	}
	return ModelInfo{}, false
}
