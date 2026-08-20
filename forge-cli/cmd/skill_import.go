package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-skills/parser"
	forgeui "github.com/initializ/forge/forge-ui"
)

// ParseSkillFrontmatterName returns the `name` from a SKILL.md frontmatter, or
// "" when it can't be parsed.
func ParseSkillFrontmatterName(skillMD string) string {
	_, meta, err := parser.ParseWithMetadata(strings.NewReader(skillMD))
	if err != nil || meta == nil {
		return ""
	}
	return meta.Name
}

// SkillImportOptions configures ImportSkillFolder.
type SkillImportOptions struct {
	// SourceDir is the folder to import: a SKILL.md plus scripts and
	// reference files.
	SourceDir string
	// AgentDir is the target agent project (must already contain forge.yaml).
	AgentDir string
	// NameOverride, when set, is used as the vendored skill name instead of
	// the frontmatter `name` / folder basename.
	NameOverride string
	// Overwrite replaces an existing skills/<name>/ directory. Without it,
	// importing over an existing skill is an error.
	Overwrite bool
}

// SkillImportResult reports what ImportSkillFolder vendored and wired, plus a
// punch-list of manual follow-ups.
type SkillImportResult struct {
	SkillName       string
	SkillDir        string   // relative to AgentDir, e.g. "skills/my-skill"
	Scripts         []string // vendored script paths (relative to the skill dir)
	ReferenceFiles  []string // vendored reference paths (relative to the skill dir)
	EgressAdded     []string
	EnvMissing      []forgeui.SkillEnvEntry
	PythonDetected  bool
	RequirementsTxt bool
	Warnings        []string
}

// scriptExtensions are the file types Forge can execute as skill scripts
// (interpreterForScript in forge-cli/tools/run_skill_script.go). A file with
// one of these extensions is vendored under the skill's scripts/ directory.
var scriptExtensions = map[string]bool{
	".sh": true, ".bash": true,
	".py": true,
	".js": true, ".cjs": true, ".mjs": true,
}

// importSkipDirs are directory names never vendored — build/venv/vcs cruft that
// would otherwise bloat the skill tree.
var importSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	".venv": true, "venv": true, "env": true,
	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true,
	"node_modules": true, ".idea": true, ".vscode": true,
}

var kebabNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const largeReferenceFileBytes = 10 << 20 // 10MB — warn (but still copy) above this

// ImportSkillFolder converts an on-disk skill folder (SKILL.md + scripts +
// reference files) into a vendored skill under AgentDir/skills/<name>/, then
// wires the skill's egress domains into forge.yaml and reports its env
// requirements. It is the reusable core behind `forge skills import`.
//
// Scripts (.sh/.py/.js) land under scripts/; every other file is copied as a
// reference preserving its relative path. All destinations are confined to the
// skill directory (no `..`/absolute escape). Python deps (requirements.txt) are
// detected and reported but not yet auto-installed — see issue #405 (D1).
func ImportSkillFolder(opts SkillImportOptions) (*SkillImportResult, error) {
	src := opts.SourceDir
	info, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("reading source folder %q: %w", src, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source %q is not a directory", src)
	}

	skillMDPath := filepath.Join(src, "SKILL.md")
	skillMD, err := os.ReadFile(skillMDPath)
	if err != nil {
		return nil, fmt.Errorf("source folder has no readable SKILL.md: %w", err)
	}

	// Verify forge.yaml is present so we're vendoring into a real agent.
	if _, err := os.Stat(filepath.Join(opts.AgentDir, "forge.yaml")); err != nil {
		return nil, fmt.Errorf("no forge.yaml in %q — run this inside an agent project (or scaffold one first)", opts.AgentDir)
	}

	name, err := resolveImportSkillName(opts.NameOverride, string(skillMD), src)
	if err != nil {
		return nil, err
	}

	skillRel := filepath.Join("skills", name)
	skillDir := filepath.Join(opts.AgentDir, skillRel)
	if _, err := os.Stat(skillDir); err == nil {
		if !opts.Overwrite {
			return nil, fmt.Errorf("skill %q already exists at %s (use --overwrite to replace)", name, skillRel)
		}
		if err := os.RemoveAll(skillDir); err != nil {
			return nil, fmt.Errorf("clearing existing skill %q: %w", name, err)
		}
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating skill directory: %w", err)
	}

	result := &SkillImportResult{SkillName: name, SkillDir: skillRel}

	// Write the SKILL.md first.
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillMD, 0o644); err != nil {
		return nil, fmt.Errorf("writing SKILL.md: %w", err)
	}

	// Vendor everything else.
	if err := vendorSkillFiles(src, skillDir, result); err != nil {
		return nil, err
	}

	// Wire egress + report env requirements from the frontmatter (reuses the
	// same helpers the Skill Builder save path uses).
	reqInfo := ParseSkillRequirements(string(skillMD))
	if len(reqInfo.EgressDomains) > 0 {
		added, mErr := MergeEgressDomains(opts.AgentDir, reqInfo.EgressDomains)
		if mErr != nil {
			result.Warnings = append(result.Warnings, "could not merge egress domains into forge.yaml: "+mErr.Error())
		} else {
			result.EgressAdded = added
		}
	}
	if reqInfo.EnvReqs != nil {
		result.EnvMissing = CheckMissingEnv(opts.AgentDir, reqInfo.EnvReqs)
	}

	// Python provisioning follow-ups (issue #405, D1).
	checkPythonProvisioning(string(skillMD), result)

	return result, nil
}

// resolveImportSkillName picks the vendored skill name: explicit override, else
// the SKILL.md frontmatter `name`, else the sanitized folder basename. The
// result must be kebab-case (the parser enforces this for the frontmatter name;
// we enforce it for the override / folder fallback too).
func resolveImportSkillName(override, skillMD, srcDir string) (string, error) {
	if override != "" {
		if !kebabNameRe.MatchString(override) {
			return "", fmt.Errorf("--name %q is not kebab-case (lowercase, digits, single hyphens)", override)
		}
		return override, nil
	}
	if info := ParseSkillFrontmatterName(skillMD); info != "" {
		return info, nil
	}
	base := sanitizeToKebab(filepath.Base(filepath.Clean(srcDir)))
	if base == "" || !kebabNameRe.MatchString(base) {
		return "", fmt.Errorf("could not derive a skill name from folder %q; set `name:` in SKILL.md or pass --name", srcDir)
	}
	return base, nil
}

// sanitizeToKebab lowercases and replaces runs of non-alphanumeric characters
// with single hyphens, trimming leading/trailing hyphens.
func sanitizeToKebab(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// vendorSkillFiles walks src and copies each file into skillDir: script-typed
// files under scripts/ (0755), everything else at its preserved relative path
// (0644). SKILL.md and skip-listed directories are excluded. All destinations
// are confined to skillDir.
func vendorSkillFiles(src, skillDir string, result *SkillImportResult) error {
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(src, path)
		if rErr != nil {
			return rErr
		}
		if rel == "." {
			return nil
		}
		// Skip build/venv/vcs directories wholesale.
		if fi.IsDir() {
			if importSkipDirs[fi.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "SKILL.md" {
			return nil // already written
		}
		if fi.Name() == ".DS_Store" {
			return nil
		}

		relSlash := filepath.ToSlash(rel)
		isScript, destRel := classifyImportFile(relSlash)

		destRel = filepath.FromSlash(destRel)
		dest := filepath.Join(skillDir, destRel)
		// Confinement: the cleaned destination must stay inside skillDir.
		if !withinDir(skillDir, dest) {
			result.Warnings = append(result.Warnings, "skipped file escaping skill dir: "+relSlash)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", destRel, err)
		}
		mode := os.FileMode(0o644)
		if isScript {
			mode = 0o755
		}
		if err := copyFileMode(path, dest, mode); err != nil {
			return fmt.Errorf("copying %s: %w", relSlash, err)
		}
		if fi.Size() > largeReferenceFileBytes && !isScript {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("large reference file vendored (%d MB): %s", fi.Size()/(1<<20), destRel))
		}
		if isScript {
			result.Scripts = append(result.Scripts, filepath.ToSlash(destRel))
		} else {
			result.ReferenceFiles = append(result.ReferenceFiles, filepath.ToSlash(destRel))
		}
		return nil
	})
}

// classifyImportFile decides where a source file (relative, slash-separated)
// lands in the skill dir and whether it's an executable script. Files already
// under scripts/ keep their path; script-typed files elsewhere are moved under
// scripts/<basename>; everything else keeps its relative path as a reference.
func classifyImportFile(relSlash string) (isScript bool, destRel string) {
	ext := strings.ToLower(filepath.Ext(relSlash))
	if strings.HasPrefix(relSlash, "scripts/") {
		return scriptExtensions[ext], relSlash
	}
	if scriptExtensions[ext] {
		return true, "scripts/" + filepath.Base(relSlash)
	}
	return false, relSlash
}

// withinDir reports whether target resolves inside base (no `..` escape).
func withinDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyFileMode copies src to dst with the given mode.
func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// checkPythonProvisioning appends warnings when a skill ships Python but its
// frontmatter or the build pipeline won't provision what it needs (issue #405).
func checkPythonProvisioning(skillMD string, result *SkillImportResult) {
	hasPython := false
	for _, s := range result.Scripts {
		if strings.HasSuffix(s, ".py") {
			hasPython = true
			break
		}
	}
	for _, f := range result.ReferenceFiles {
		if filepath.Base(f) == "requirements.txt" {
			result.RequirementsTxt = true
		}
	}
	if !hasPython {
		return
	}
	result.PythonDetected = true

	lower := strings.ToLower(skillMD)
	if !strings.Contains(lower, "python3") {
		result.Warnings = append(result.Warnings,
			"Python scripts detected but SKILL.md `metadata.forge.requires.bins` does not list python3 — add python3 (and pip) so the built image provisions the interpreter.")
	}
	if result.RequirementsTxt {
		result.Warnings = append(result.Warnings,
			"requirements.txt vendored, but per-skill pip install is not wired yet (issue #405, D1) — declare its packages as system bins in requires.bins, or install them in your base image, until that lands.")
	}
}
