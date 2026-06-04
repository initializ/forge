package runtime

import (
	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/types"
)

// ProtocolVersion is the A2A protocol version every AgentCard claims to
// conform to. Pinned at build time. Bumping is a deliberate PR, not a
// runtime negotiation — same discipline Forge uses for the MCP protocol
// version pin.
const ProtocolVersion = "0.3.0"

// defaultInputModes / defaultOutputModes are the MIME types Forge agents
// accept and emit when a skill doesn't override them. text/plain covers
// human-language messages; application/json covers structured tool args
// and tool results.
var (
	defaultInputModes  = []string{"text/plain", "application/json"}
	defaultOutputModes = []string{"text/plain", "application/json"}
)

// AgentCardFromSpec constructs an AgentCard from an AgentSpec and a base URL.
// The baseURL should be a fully-formed URL (e.g. "http://localhost:8080").
//
// Per A2A 0.3.0 the card requires `version`, `protocolVersion`,
// `defaultInputModes`, and `defaultOutputModes`. The function fills
// those from the spec / config defaults; callers can override after
// construction (e.g. the runner enriches with SecuritySchemes derived
// from the auth chain).
func AgentCardFromSpec(spec *agentspec.AgentSpec, baseURL string) *a2a.AgentCard {
	card := &a2a.AgentCard{
		Name:               firstNonEmpty(spec.Name, spec.AgentID),
		Description:        spec.Description,
		URL:                baseURL,
		Version:            firstNonEmpty(spec.Version, "0.0.0"),
		ProtocolVersion:    ProtocolVersion,
		DefaultInputModes:  defaultInputModes,
		DefaultOutputModes: defaultOutputModes,
	}

	// Convert tools to skills. Tools surfaced as A2A skills carry the
	// "tool" tag so downstream clients can distinguish tool-shaped
	// skills from SKILL.md skills.
	for _, t := range spec.Tools {
		card.Skills = append(card.Skills, a2a.Skill{
			ID:          t.Name,
			Name:        t.Name,
			Description: t.Description,
			Tags:        []string{"tool"},
		})
	}

	// Copy A2A capabilities + skills from the spec's A2A block.
	if spec.A2A != nil {
		for _, s := range spec.A2A.Skills {
			tags := s.Tags
			if len(tags) == 0 {
				// A2A 0.3.0 makes tags REQUIRED. If the spec doesn't
				// supply any, fall back to "skill" so the field is
				// always non-empty.
				tags = []string{"skill"}
			}
			card.Skills = append(card.Skills, a2a.Skill{
				ID:          s.ID,
				Name:        s.Name,
				Description: s.Description,
				Tags:        tags,
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
//
// Used when no build-time AgentSpec is available (e.g. local `forge dev`
// from a freshly-scaffolded project). Identical conformance to A2A 0.3.0
// as the spec-derived path.
func AgentCardFromConfig(cfg *types.ForgeConfig, baseURL string) *a2a.AgentCard {
	card := &a2a.AgentCard{
		Name:               cfg.AgentID,
		URL:                baseURL,
		Version:            firstNonEmpty(cfg.Version, "0.0.0"),
		ProtocolVersion:    ProtocolVersion,
		DefaultInputModes:  defaultInputModes,
		DefaultOutputModes: defaultOutputModes,
	}

	for _, t := range cfg.Tools {
		card.Skills = append(card.Skills, a2a.Skill{
			ID:   t.Name,
			Name: t.Name,
			Tags: []string{"tool"},
		})
	}

	return card
}

// firstNonEmpty returns the first non-empty string from its arguments,
// or "" if all are empty. Used for required-field defaulting.
func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
