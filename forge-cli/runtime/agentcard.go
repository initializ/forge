package runtime

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// agentCardBaseURL is the URL the Agent Card advertises in its `url` field. When
// the agent is fronted by an ingress/gateway, the operator sets FORGE_A2A_PUBLIC_URL
// to the externally-reachable origin (e.g. https://agent.example.com) so A2A clients
// that dial the card's `url` reach the agent — not the pod-local default. Unset (the
// standalone / local-dev case) falls back to http://localhost:<port>.
//
// A scheme-less or otherwise malformed override is rejected with a warning and the
// local fallback, so a misconfiguration surfaces loudly at startup instead of
// silently handing A2A clients an undialable card url.
func agentCardBaseURL(port int) string {
	fallback := fmt.Sprintf("http://localhost:%d", port)
	u := strings.TrimRight(strings.TrimSpace(os.Getenv("FORGE_A2A_PUBLIC_URL")), "/")
	if u == "" {
		return fallback
	}
	if pu, err := url.Parse(u); err != nil || pu.Scheme == "" || pu.Host == "" {
		fmt.Fprintf(os.Stderr, "forge: FORGE_A2A_PUBLIC_URL=%q is not a valid absolute URL (want scheme://host); advertising %q in the Agent Card instead\n", u, fallback)
		return fallback
	}
	return u
}

// BuildAgentCard constructs an AgentCard from available sources.
// It first tries .forge-output/agent.json; if that doesn't exist, it falls
// back to the ForgeConfig.
func BuildAgentCard(workDir string, cfg *types.ForgeConfig, port int) (*a2a.AgentCard, error) {
	baseURL := agentCardBaseURL(port)

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
