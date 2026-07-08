package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
	skillparser "github.com/initializ/forge/forge-skills/parser"
)

// ReadSkillTool reads a skill's SKILL.md file on demand.
// This enables lazy-loading: the LLM only loads full skill instructions
// when it decides a skill is relevant.
type ReadSkillTool struct {
	workDir string
}

// NewReadSkillTool creates a read_skill tool rooted at the given working directory.
func NewReadSkillTool(workDir string) *ReadSkillTool {
	return &ReadSkillTool{workDir: workDir}
}

func (t *ReadSkillTool) Name() string             { return "read_skill" }
func (t *ReadSkillTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *ReadSkillTool) Description() string {
	return "Read the full instructions for an available skill. " +
		"Pass the skill name exactly as listed under 'Available Skills'. " +
		"Returns the skill's SKILL.md content with usage details, parameters, and examples. " +
		"Pass the optional 'file' argument to read a specific file relative to the skill's directory " +
		"(e.g. a reference doc the instructions point to) instead of SKILL.md."
}

func (t *ReadSkillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name exactly as shown in the Available Skills list (e.g. 'k8s-incident-triage')"},
			"file": {"type": "string", "description": "Optional. A file path RELATIVE TO THE SKILL's directory to read instead of SKILL.md (e.g. 'reference/runbook.md', 'owl.md'). Use for skill instructions like 'read reference/runbook.md'."}
		},
		"required": ["name"]
	}`)
}

func (t *ReadSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Name string `json:"name"`
		File string `json:"file"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing read_skill input: %w", err)
	}
	if input.Name == "" {
		return `{"error": "name is required"}`, nil
	}

	// Security: prevent directory traversal.
	if strings.ContainsAny(input.Name, `/\`) || strings.Contains(input.Name, "..") {
		return `{"error": "invalid skill name"}`, nil
	}

	// Skill-relative file read: a SKILL.md may instruct "read
	// reference/runbook.md" — resolve that path against the skill's own
	// directory (confined; no escaping) and return its contents.
	if input.File != "" {
		dir, ok := SkillDir(t.workDir, input.Name)
		if !ok {
			if _, available := t.buildNameIndex(); len(available) > 0 {
				list, _ := json.Marshal(available)
				return fmt.Sprintf(`{"error": "skill %q not found", "available_skills": %s}`, input.Name, list), nil
			}
			return fmt.Sprintf(`{"error": "skill %q not found"}`, input.Name), nil
		}
		full, err := SafeSkillJoin(dir, input.File)
		if err != nil {
			return `{"error": "invalid file path (must stay within the skill directory)"}`, nil
		}
		data, err := os.ReadFile(full) //nolint:gosec // confined to the skill dir by SafeSkillJoin
		if err != nil {
			return fmt.Sprintf(`{"error": "file %q not found in skill %q"}`, input.File, input.Name), nil
		}
		return string(data), nil
	}

	// Resolve the SKILL.md path: direct filesystem lookup first, then a
	// frontmatter-name index so the requested name resolves even when the
	// skill directory differs from the skill's frontmatter name.
	path := t.resolvePath(input.Name)
	if path == "" {
		// Not found — return the available skill names so the model can
		// retry with a valid identifier instead of giving up.
		if _, available := t.buildNameIndex(); len(available) > 0 {
			list, _ := json.Marshal(available)
			return fmt.Sprintf(`{"error": "skill %q not found", "available_skills": %s}`, input.Name, list), nil
		}
		return fmt.Sprintf(`{"error": "skill %q not found"}`, input.Name), nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // path derived from workDir scan / sanitized name
	if err != nil {
		return fmt.Sprintf(`{"error": "skill %q not found"}`, input.Name), nil
	}
	content := string(data)

	// Surface the rest of the skill's directory — helper scripts (shell,
	// python, javascript, ...), reference material, and additional
	// markdown all live alongside SKILL.md and reach the running agent
	// (COPY . . at build time), but are invisible unless listed. The
	// model can then read or execute them as the skill's steps describe.
	if footer := t.skillFilesFooter(path); footer != "" {
		content += "\n\n" + footer
	}
	return content, nil
}

// resolvePath returns the SKILL.md path for the requested name, or "" if
// no skill resolves. Tries the on-disk layouts (and the underscore->hyphen
// variant) first, then the normalized frontmatter-name index.
func (t *ReadSkillTool) resolvePath(name string) string {
	variants := []string{name}
	if hyphenated := strings.ReplaceAll(name, "_", "-"); hyphenated != name {
		variants = append(variants, hyphenated)
	}
	for _, n := range variants {
		if p := filepath.Join(t.workDir, "skills", n+".md"); fileExists(p) {
			return p
		}
		if p := filepath.Join(t.workDir, "skills", n, "SKILL.md"); fileExists(p) {
			return p
		}
	}
	if index, _ := t.buildNameIndex(); index != nil {
		if p, ok := index[normalizeSkillKey(name)]; ok {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// SkillDir resolves a skill's loadable name to its on-disk directory
// (`<workDir>/skills/<dir>`), matching read_skill's resolution: the
// directory name (and its underscore->hyphen variant) first, then the
// frontmatter `name` of any subdirectory skill (normalized for case and
// separator drift). Returns ("", false) for an unknown name or a flat
// single-file skill (which has no companion directory). Shared with the
// run_skill_script tool so skill-relative reads and executions resolve
// the same skill dir.
func SkillDir(workDir, name string) (string, bool) {
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", false
	}
	skillsDir := filepath.Join(workDir, "skills")
	variants := []string{name}
	if h := strings.ReplaceAll(name, "_", "-"); h != name {
		variants = append(variants, h)
	}
	for _, n := range variants {
		dir := filepath.Join(skillsDir, n)
		if fileExists(filepath.Join(dir, "SKILL.md")) {
			return dir, true
		}
	}
	// Frontmatter-name / normalized match across subdirectory skills.
	ents, err := os.ReadDir(skillsDir)
	if err != nil {
		return "", false
	}
	want := normalizeSkillKey(name)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		md := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if !fileExists(md) {
			continue
		}
		if normalizeSkillKey(e.Name()) == want || normalizeSkillKey(frontmatterName(md)) == want {
			return filepath.Join(skillsDir, e.Name()), true
		}
	}
	return "", false
}

// SafeSkillJoin joins a skill-relative path onto the skill directory and
// confines the result to that directory — rejecting absolute paths, `..`
// traversal, AND symlinks that resolve outside the skill. The symlink
// check matters because a skill package ships whatever the author writes
// (COPY . .): a bundled symlink like `skills/owl/leak -> /etc/shadow`
// passes a purely-textual guard (no `..`, not absolute) but `os.ReadFile`
// / the script interpreter would follow it out of the skill dir. Same
// confinement posture as read_skill's traversal guard and cli_execute's
// workdir confinement.
func SafeSkillJoin(skillDir, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("path must be relative to the skill directory")
	}
	full := filepath.Join(skillDir, rel)
	// Textual guard first (cheap; also handles a target that doesn't exist).
	within, err := filepath.Rel(skillDir, full)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the skill directory")
	}
	// Symlink guard: resolve symlinks on both the skill dir and the deepest
	// existing part of the target, then re-check containment against the
	// resolved root. Catches a leaf symlink (target exists) and an
	// intermediate symlinked directory (target's parent exists).
	root, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		return "", errors.New("resolving skill directory: " + err.Error())
	}
	// Anchor to the RESOLVED root so a symlinked temp root (e.g. macOS
	// /var -> /private/var) doesn't cause a spurious prefix mismatch when
	// the target doesn't exist yet.
	resolved := filepath.Join(root, within)
	if r, e := filepath.EvalSymlinks(full); e == nil {
		resolved = r
	} else if r, e := filepath.EvalSymlinks(filepath.Dir(full)); e == nil {
		resolved = filepath.Join(r, filepath.Base(full))
	}
	rw, err := filepath.Rel(root, resolved)
	if err != nil || rw == ".." || strings.HasPrefix(rw, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the skill directory (symlink)")
	}
	return full, nil
}

// scriptLang maps a file extension to the interpreter language, for
// annotating helper scripts in the file listing. Mirrors the languages
// forge recognizes for tool scripts.
func scriptLang(ext string) string {
	switch strings.ToLower(ext) {
	case ".sh", ".bash":
		return "shell"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	default:
		return ""
	}
}

// skillFilesFooter lists the other files living in a subdirectory skill's
// folder (everything except the SKILL.md itself), so the model knows the
// skill ships helper scripts and reference material it can read or run.
// Paths are relative to the agent working directory so the model can pass
// them straight to file tools. Returns "" for flat (single-file) skills
// or when the folder holds nothing else.
func (t *ReadSkillTool) skillFilesFooter(skillMdPath string) string {
	if filepath.Base(skillMdPath) != "SKILL.md" {
		return "" // flat skills/<name>.md layout has no companion directory
	}
	dir := filepath.Dir(skillMdPath)
	var files []string
	const maxFiles = 200
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(files) >= maxFiles {
			return nil
		}
		if p == skillMdPath {
			return nil
		}
		rel, relErr := filepath.Rel(t.workDir, p)
		if relErr != nil {
			rel = p
		}
		if lang := scriptLang(filepath.Ext(p)); lang != "" {
			files = append(files, rel+" — "+lang)
		} else {
			files = append(files, rel)
		}
		return nil
	})
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	var b strings.Builder
	b.WriteString("## Skill files\n")
	b.WriteString("This skill ships the following supporting files (relative to the agent root). ")
	b.WriteString("Read or execute them via your file/exec tools as the steps above describe:\n")
	for _, f := range files {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteString("\n")
	}
	return b.String()
}

// buildNameIndex scans the skills directory and returns:
//   - a map from normalized skill key -> SKILL.md path, keyed by BOTH the
//     frontmatter `name` and the directory/file name of each skill;
//   - a sorted, de-duplicated list of the loadable skill names for the
//     "not found" hint (frontmatter name preferred, else directory/file).
func (t *ReadSkillTool) buildNameIndex() (map[string]string, []string) {
	index := make(map[string]string)
	displaySet := make(map[string]struct{})
	skillsDir := filepath.Join(t.workDir, "skills")
	ents, err := os.ReadDir(skillsDir)
	if err != nil {
		return index, nil
	}
	add := func(path, dirOrBase string) {
		display := dirOrBase
		if name := frontmatterName(path); name != "" {
			index[normalizeSkillKey(name)] = path
			display = name
		}
		index[normalizeSkillKey(dirOrBase)] = path
		displaySet[display] = struct{}{}
	}
	for _, e := range ents {
		if e.IsDir() {
			p := filepath.Join(skillsDir, e.Name(), "SKILL.md")
			if _, statErr := os.Stat(p); statErr == nil {
				add(p, e.Name())
			}
			continue
		}
		// Flat format: skills/{name}.md (skip the scripts/ helper dir).
		if strings.HasSuffix(e.Name(), ".md") {
			p := filepath.Join(skillsDir, e.Name())
			add(p, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	display := make([]string, 0, len(displaySet))
	for name := range displaySet {
		display = append(display, name)
	}
	sort.Strings(display)
	return index, display
}

// frontmatterName parses just the frontmatter `name` from a SKILL.md
// file. Returns "" when the file can't be read or has no name.
func frontmatterName(path string) string {
	f, err := os.Open(path) //nolint:gosec // path derived from workDir scan
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	_, meta, err := skillparser.ParseWithMetadata(f)
	if err != nil || meta == nil {
		return ""
	}
	return strings.TrimSpace(meta.Name)
}

// normalizeSkillKey lowercases and unifies underscore/hyphen so that
// "K8s_Triage", "k8s-triage" and "k8s_triage" all collide. It does NOT
// bridge semantically different names — the catalog advertises the exact
// loadable name, and this only absorbs case/separator drift from the LLM.
func normalizeSkillKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "_", "-")
}
