package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// BuildAgentCard constructs an AgentCard from available sources.
// It first tries .forge-output/agent.json; if that doesn't exist, it falls
// back to the ForgeConfig.
func BuildAgentCard(workDir string, cfg *types.ForgeConfig, port int) (*a2a.AgentCard, error) {
	baseURL := fmt.Sprintf("http://localhost:%d", port)

	// Try loading from a prior build
	card, err := agentCardFromDisk(workDir, baseURL)
	if err == nil && card != nil {
		return card, nil
	}

	// Fall back to forge.yaml config
	return coreruntime.AgentCardFromConfig(cfg, baseURL), nil
}

func agentCardFromDisk(workDir string, baseURL string) (*a2a.AgentCard, error) {
	path := filepath.Join(workDir, ".forge-output", "agent.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var spec agentspec.AgentSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing agent.json: %w", err)
	}

	return coreruntime.AgentCardFromSpec(&spec, baseURL), nil
}
