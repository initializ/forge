package runtime

import (
	"sort"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-skills/contract"
)

// AppendSkillsFromDescriptors maps the runtime's SkillDescriptor list
// (sourced from the embedded + local skill registries) into A2A
// AgentSkill objects and appends them to the card. Skill IDs already
// present on the card are skipped so this is safe to call after
// AgentCardFromSpec / AgentCardFromConfig — those populate the card
// from build-time artifacts; this fills in any runtime-registered
// skills the build artifact didn't cover.
//
// Mapping (Forge SKILL.md → A2A AgentSkill):
//
//	SkillDescriptor.Name        → Skill.ID + Skill.Name
//	SkillDescriptor.DisplayName → Skill.Name (when present)
//	SkillDescriptor.Description → Skill.Description
//	SkillDescriptor.Category    → Skill.Tags[0] (when present)
//	SkillDescriptor.Tags        → Skill.Tags (appended)
//
// A2A 0.3.0 requires Tags to be non-empty; when neither category nor
// tags are set, we fall back to ["skill"] so the field is always
// populated.
//
// Forge-internal fields (RequiredEnv, RequiredBins, EgressDomains,
// DeniedTools, TimeoutHint, Provenance) are intentionally NOT mapped
// into the card. The Agent Card is a public discovery surface; those
// fields are runtime contracts that stay inside Forge.
func AppendSkillsFromDescriptors(card *a2a.AgentCard, descs []contract.SkillDescriptor) {
	if card == nil || len(descs) == 0 {
		return
	}
	seen := map[string]struct{}{}
	for _, s := range card.Skills {
		seen[s.ID] = struct{}{}
	}

	for _, d := range descs {
		if d.Name == "" {
			continue
		}
		if _, dup := seen[d.Name]; dup {
			continue
		}
		card.Skills = append(card.Skills, skillFromDescriptor(d))
		seen[d.Name] = struct{}{}
	}

	// Deterministic order: skills sort by ID. This keeps the
	// /.well-known/agent-card.json bytes stable across restarts so
	// downstream consumers can hash the card for change detection
	// (and so the agent_card_published audit event's hash is
	// deterministic per agent configuration).
	sort.SliceStable(card.Skills, func(i, j int) bool {
		return card.Skills[i].ID < card.Skills[j].ID
	})
}

// skillFromDescriptor builds one A2A Skill from a Forge SkillDescriptor.
// Pure function — no I/O, easy to test.
func skillFromDescriptor(d contract.SkillDescriptor) a2a.Skill {
	displayName := d.DisplayName
	if displayName == "" {
		displayName = d.Name
	}

	// Tags: category first (so clients can group), then any extra tags
	// from frontmatter. Dedup case-insensitively.
	var tags []string
	added := map[string]struct{}{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		k := strings.ToLower(t)
		if _, ok := added[k]; ok {
			return
		}
		added[k] = struct{}{}
		tags = append(tags, t)
	}
	add(d.Category)
	for _, t := range d.Tags {
		add(t)
	}
	if len(tags) == 0 {
		tags = []string{"skill"}
	}

	return a2a.Skill{
		ID:          d.Name,
		Name:        displayName,
		Description: d.Description,
		Tags:        tags,
	}
}
