package build

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	cliskills "github.com/initializ/forge/forge-cli/skills"
	"github.com/initializ/forge/forge-core/agentspec"
)

// populateA2ASkillsFromSKILLmd walks the agent's SKILL.md files at
// build time, parses their frontmatter, and writes the discovered
// skills into `spec.A2A.Skills`. Mirrors the runtime enrichment in
// `forge-cli/runtime/runner_agentcard_skills.go` so the published
// AgentSpec (consumed by initializ-side registries and any A2A client
// that reads `agent.json` directly) carries the same skill list the
// runtime's `/.well-known/agent-card.json` advertises.
//
// File discovery mirrors `Runner.discoverSkillFiles`:
//
//	skills/*.md         — flat-format skill files
//	skills/*/SKILL.md   — subdirectory-format skill files
//	<skills.path>       — main agent skill (default "SKILL.md")
//
// Skills with no `metadata.name` (or `name`) frontmatter field are
// skipped — they have no A2A-stable identity to advertise. Parsing
// failures are tolerated silently; the runner's own startup pass
// surfaces parse errors with full context.
//
// Output is deterministically ordered by ID so `agent.json` is
// byte-stable across rebuilds.
func populateA2ASkillsFromSKILLmd(spec *agentspec.AgentSpec, workDir, skillsPath string) {
	skills := discoverBuildTimeSkills(workDir, skillsPath)
	if len(skills) == 0 {
		return
	}
	if spec.A2A == nil {
		spec.A2A = &agentspec.A2AConfig{}
	}
	spec.A2A.Skills = append(spec.A2A.Skills, skills...)
	sort.SliceStable(spec.A2A.Skills, func(i, j int) bool {
		return spec.A2A.Skills[i].ID < spec.A2A.Skills[j].ID
	})
}

// discoverBuildTimeSkills is the file-walk + parse + map sequence
// extracted for testability. Returns deduplicated A2ASkill objects
// keyed by ID; the first occurrence of each ID wins.
func discoverBuildTimeSkills(workDir, skillsPath string) []agentspec.A2ASkill {
	files := discoverSkillFilePaths(workDir, skillsPath)
	if len(files) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []agentspec.A2ASkill
	for _, p := range files {
		_, meta, err := cliskills.ParseFileWithMetadata(p)
		if err != nil || meta == nil {
			continue
		}
		id := strings.TrimSpace(meta.Name)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		// Tags: category first (so A2A clients can group skills),
		// then any frontmatter tags. A2A 0.3.0 requires non-empty tags
		// on AgentSkill — the runtime's a2a.AgentCard mapper defaults
		// missing tags to ["skill"], so we don't have to here.
		var tags []string
		if cat := strings.TrimSpace(meta.Category); cat != "" {
			tags = append(tags, cat)
		}
		tags = append(tags, meta.Tags...)

		out = append(out, agentspec.A2ASkill{
			ID:          id,
			Name:        firstNonEmptyBuild(meta.Name, id),
			Description: strings.TrimSpace(meta.Description),
			Tags:        tags,
		})
	}
	return out
}

// discoverSkillFilePaths returns the same set of SKILL.md file paths
// the runner's `discoverSkillFiles` collects. Kept here (rather than
// shared with the runner) because the build pipeline runs without a
// Runner instance — duplicating ~15 lines beats a circular
// import or a new shared package.
func discoverSkillFilePaths(workDir, skillsPath string) []string {
	skillsDir := filepath.Join(workDir, "skills")

	matches, _ := filepath.Glob(filepath.Join(skillsDir, "*.md"))

	subMatches, _ := filepath.Glob(filepath.Join(skillsDir, "*", "SKILL.md"))
	matches = append(matches, subMatches...)

	main := skillsPath
	if main == "" {
		main = "SKILL.md"
	}
	if !filepath.IsAbs(main) {
		main = filepath.Join(workDir, main)
	}
	if info, err := os.Stat(main); err == nil && !info.IsDir() {
		matches = append(matches, main)
	}
	return matches
}

func firstNonEmptyBuild(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
