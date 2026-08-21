package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-cli/runtime"
	cliskills "github.com/initializ/forge/forge-cli/skills"
	coretools "github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-skills/contract"
)

// skillToolFinding is one problem the tool→registry lint surfaces at author or
// build time — a failure the runtime would otherwise handle silently at
// startup (an error log line at best), which is exactly what customers had to
// reimplement as external lints against Forge's parser (#418).
type skillToolFinding struct {
	Skill string // skill directory name, or the SKILL.md file's base name
	Tool  string // `## Tool:` name ("" for orphan-script findings)
	Level string // "error" | "warn"
	Msg   string
}

// lintSkillTools scans the skill tree rooted at workDir and reports tool
// registration problems that the runtime resolves silently:
//
//   - a `## Tool:` whose `**Input:**` yields a JSON-schema property key outside
//     the provider pattern ^[a-zA-Z0-9_.-]{1,64}$ — the runtime DROPS the whole
//     tool (registering it would 400 every LLM call). Reported as an error.
//   - a script-runtime `## Tool:` with no backing scripts/<name>.{sh,py,js}
//     (either name form) — the tool never registers. Reported as an error.
//   - a scripts/*.{sh,py,js} with no `## Tool:` heading claiming it — reachable
//     only by path via run_skill_script; dead weight if unreferenced. Warning.
//
// It uses runtime.SkillScriptCandidatePaths so the binding check matches the
// runtime resolver exactly (the two cannot drift). mainSkillPath is the
// resolved main SKILL.md (root or forge.yaml skills.path).
func lintSkillTools(workDir, mainSkillPath string) []skillToolFinding {
	var findings []skillToolFinding

	// claimed collects every candidate script path (workDir-relative) that a
	// script-runtime `## Tool:` could bind to, so the orphan pass can tell a
	// backing script from dead weight regardless of which name form it uses.
	claimed := map[string]bool{}
	// scriptDirs collects the scripts/ directories to sweep for orphans.
	scriptDirs := map[string]bool{}

	for _, file := range discoverSkillFilesForLint(workDir, mainSkillPath) {
		entries, meta, err := cliskills.ParseFileWithMetadata(file)
		if err != nil {
			continue
		}

		skillDirName := ""
		if strings.HasSuffix(file, string(filepath.Separator)+"SKILL.md") || strings.HasSuffix(file, "/SKILL.md") {
			skillDirName = filepath.Base(filepath.Dir(file))
		}
		label := skillDirName
		if label == "" {
			label = filepath.Base(file)
		}

		runtimeMode := skillRuntimeMode(meta)

		// Register the scripts/ directories this skill could draw from.
		if skillDirName != "" {
			scriptDirs[filepath.Join("skills", skillDirName, "scripts")] = true
		}
		scriptDirs[filepath.Join("skills", "scripts")] = true

		for _, entry := range entries {
			// 1. Invalid **Input:** property key(s) — parsed exactly as the
			// runtime does, so the rule can't drift from enforcement.
			if bad := coretools.InvalidSchemaPropertyKeys(coretools.InputSpecToSchema(entry.InputSpec)); len(bad) > 0 {
				findings = append(findings, skillToolFinding{
					Skill: label, Tool: entry.Name, Level: "error",
					Msg: "input property key(s) " + strings.Join(bad, ", ") +
						" violate ^[a-zA-Z0-9_.-]{1,64}$ — the tool is DROPPED at runtime (it would fail every LLM call)",
				})
			}

			// Binary-runtime tools bind to requires.bins, not a script — the
			// binary/env checks above cover them.
			if runtimeMode == contract.SkillRuntimeBinary {
				continue
			}

			// 2. Tool→script binding — same candidate set the runtime resolves.
			candidates := runtime.SkillScriptCandidatePaths(skillDirName, entry.Name)
			backed := false
			for _, cand := range candidates {
				claimed[cand] = true
				if _, err := os.Stat(filepath.Join(workDir, cand)); err == nil {
					backed = true
				}
			}
			if !backed {
				findings = append(findings, skillToolFinding{
					Skill: label, Tool: entry.Name, Level: "error",
					Msg: "no backing script (scripts/" + entry.Name + ".{sh,py,js}) — the tool will not register",
				})
			}
		}
	}

	// 3. Orphan scripts — a scripts/*.<ext> no `## Tool:` heading claims.
	exts := map[string]bool{}
	for _, e := range runtime.SkillScriptExtensions() {
		exts[e] = true
	}
	for dir := range scriptDirs {
		listed, err := os.ReadDir(filepath.Join(workDir, dir))
		if err != nil {
			continue
		}
		for _, de := range listed {
			if de.IsDir() || !exts[strings.ToLower(filepath.Ext(de.Name()))] {
				continue
			}
			rel := filepath.Join(dir, de.Name())
			if claimed[rel] {
				continue
			}
			findings = append(findings, skillToolFinding{
				Skill: skillLabelForScriptDir(dir), Level: "warn",
				Msg: "orphan script " + rel + " — no `## Tool:` heading binds it; reachable only via run_skill_script (dead weight if unreferenced)",
			})
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Skill != findings[j].Skill {
			return findings[i].Skill < findings[j].Skill
		}
		if findings[i].Level != findings[j].Level {
			return findings[i].Level < findings[j].Level // "error" before "warn"
		}
		return findings[i].Tool < findings[j].Tool
	})
	return findings
}

// discoverSkillFilesForLint mirrors Runner.discoverSkillFiles: flat skills/*.md,
// subdir skills/*/SKILL.md, plus the main SKILL.md (root or forge.yaml path).
func discoverSkillFilesForLint(workDir, mainSkillPath string) []string {
	skillsDir := filepath.Join(workDir, "skills")
	matches, _ := filepath.Glob(filepath.Join(skillsDir, "*.md"))
	subDir, _ := filepath.Glob(filepath.Join(skillsDir, "*", "SKILL.md"))
	matches = append(matches, subDir...)
	if info, err := os.Stat(mainSkillPath); err == nil && !info.IsDir() {
		matches = append(matches, mainSkillPath)
	}
	return matches
}

// skillRuntimeMode reads metadata.forge.runtime, defaulting to "script".
func skillRuntimeMode(meta *contract.SkillMetadata) string {
	mode := contract.SkillRuntimeScript
	if meta != nil && meta.Metadata != nil {
		if forgeMap, ok := meta.Metadata["forge"]; ok {
			if raw, ok := forgeMap["runtime"]; ok {
				if s, ok := raw.(string); ok && s != "" {
					mode = s
				}
			}
		}
	}
	return mode
}

// skillLabelForScriptDir turns "skills/foo/scripts" into "foo" and the shared
// "skills/scripts" into "(shared)" for readable orphan-finding labels.
func skillLabelForScriptDir(dir string) string {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	if len(parts) == 3 && parts[0] == "skills" && parts[2] == "scripts" {
		return parts[1]
	}
	return "(shared)"
}
