package runtime

import (
	"strings"

	cliskills "github.com/initializ/forge/forge-cli/skills"
	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-skills/contract"
)

// enrichAgentCardWithSkills walks the agent's SKILL.md files
// (`discoverSkillFiles()`) at runtime, parses their frontmatter, and
// appends each parsed skill onto the published Agent Card.
//
// Why this lives in the runner rather than the build pipeline:
//
//   - `forge build` calls `compiler.ConfigToAgentSpec` which produces a
//     spec with `A2A: nil` — it never walks SKILL.md. So the
//     `AgentCardFromSpec(spec, baseURL)` path that the runner uses
//     post-build also wouldn't see skills without this enrichment.
//   - The runner already discovers SKILL.md files for every other
//     purpose (tool registration, skill-guardrail loading, etc.) — it
//     has the parser, the workDir, and the frontmatter at hand.
//   - Using the same enrichment at both `forge dev` (no build artifact)
//     and `forge run` (post-build) means the card's skill list is
//     identical in both environments. Issue #85 explicitly calls for
//     "card endpoint behavior identical in forge dev (local) and
//     deployed modes."
//
// Mapping rules + dedup semantics live in
// `coreruntime.AppendSkillsFromDescriptors`. Pre-existing skills on the
// card (from `agentspec.A2A.Skills` when that's ever populated, or
// builtin tools surfaced via `AgentCardFromSpec`/`AgentCardFromConfig`)
// take precedence — this function only adds skills that aren't already
// represented.
func (r *Runner) enrichAgentCardWithSkills(card *a2a.AgentCard) {
	if card == nil {
		return
	}
	descs := r.skillDescriptorsForCard()
	if len(descs) == 0 {
		return
	}
	coreruntime.AppendSkillsFromDescriptors(card, descs)
}

// skillDescriptorsForCard walks every SKILL.md file the runner can
// see and converts each parsed frontmatter to a SkillDescriptor
// shaped for the agent-card mapping. SKILL.md files without a
// `metadata.name` (or top-level `name`) frontmatter field are skipped
// — they have no A2A-stable identity to advertise.
//
// Parsing errors are tolerated silently here: the agent card is a
// best-effort discovery surface; a malformed SKILL.md should not
// prevent agent startup. The same files get parsed elsewhere in the
// startup sequence with their own error reporting.
func (r *Runner) skillDescriptorsForCard() []contract.SkillDescriptor {
	files := r.discoverSkillFiles()
	if len(files) == 0 {
		return nil
	}
	var out []contract.SkillDescriptor
	for _, path := range files {
		_, meta, err := cliskills.ParseFileWithMetadata(path)
		if err != nil || meta == nil {
			continue
		}
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			continue
		}
		out = append(out, contract.SkillDescriptor{
			Name:        name,
			Description: strings.TrimSpace(meta.Description),
			Category:    strings.TrimSpace(meta.Category),
			Tags:        meta.Tags,
		})
	}
	return out
}
