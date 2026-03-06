package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/agentspec"
)

// LoadPolicyScaffold reads policy-scaffold.json from the output directory.
// Returns nil (no error) if the file does not exist.
func LoadPolicyScaffold(workDir string) (*agentspec.PolicyScaffold, error) {
	path := filepath.Join(workDir, ".forge-output", "policy-scaffold.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ps agentspec.PolicyScaffold
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("parsing policy scaffold: %w", err)
	}
	return &ps, nil
}

// DefaultPolicyScaffold returns a scaffold with all built-in guardrails enabled.
// Used when no policy-scaffold.json exists (e.g. running without forge build).
func DefaultPolicyScaffold() *agentspec.PolicyScaffold {
	return &agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{
			{
				Type:   "content_filter",
				Config: map[string]any{"enabled": true},
			},
			{Type: "no_pii"},
			{Type: "jailbreak_protection"},
			{Type: "no_secrets"},
		},
	}
}
