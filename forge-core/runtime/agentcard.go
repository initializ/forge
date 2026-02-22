package runtime

import (
	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/types"
)

// AgentCardFromSpec constructs an AgentCard from an AgentSpec and a base URL.
// The baseURL should be a fully-formed URL (e.g. "http://localhost:8080").
func AgentCardFromSpec(spec *agentspec.AgentSpec, baseURL string) *a2a.AgentCard {
	card := &a2a.AgentCard{
		Name:        spec.Name,
		Description: spec.Description,
		URL:         baseURL,
	}

	// Convert tools to skills
	for _, t := range spec.Tools {
		card.Skills = append(card.Skills, a2a.Skill{
			ID:          t.Name,
			Name:        t.Name,
			Description: t.Description,
		})
	}

	// Copy A2A capabilities if present
	if spec.A2A != nil {
		for _, s := range spec.A2A.Skills {
			card.Skills = append(card.Skills, a2a.Skill{
				ID:          s.ID,
				Name:        s.Name,
				Description: s.Description,
				Tags:        s.Tags,
			})
		}
		if spec.A2A.Capabilities != nil {
			card.Capabilities = &a2a.AgentCapabilities{
				Streaming:              spec.A2A.Capabilities.Streaming,
				PushNotifications:      spec.A2A.Capabilities.PushNotifications,
				StateTransitionHistory: spec.A2A.Capabilities.StateTransitionHistory,
			}
		}
	}

	return card
}

// AgentCardFromConfig constructs an AgentCard from a ForgeConfig and a base URL.
// The baseURL should be a fully-formed URL (e.g. "http://localhost:8080").
func AgentCardFromConfig(cfg *types.ForgeConfig, baseURL string) *a2a.AgentCard {
	card := &a2a.AgentCard{
		Name: cfg.AgentID,
		URL:  baseURL,
	}

	for _, t := range cfg.Tools {
		card.Skills = append(card.Skills, a2a.Skill{
			ID:   t.Name,
			Name: t.Name,
		})
	}

	return card
}
