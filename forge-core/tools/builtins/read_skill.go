package builtins

import (
	"context"
	"encoding/json"
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
		"Returns the skill's SKILL.md content with usage details, parameters, and examples."
}

func (t *ReadSkillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name exactly as shown in the Available Skills list (e.g. 'k8s-incident-triage')"}
		},
		"required": ["name"]
	}`)
}

func (t *ReadSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Name string `json:"name"`
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

	// 1) Fast path: direct filesystem lookup by the requested name and
	//    its underscore->hyphen variant.
	if data, ok := t.readByName(input.Name); ok {
		return string(data), nil
	}

	// 2) Index path: match the requested name against every skill's
	//    frontmatter `name` (the loadable identifier the catalog and the
	//    agent card advertise) and its directory/file name, normalized so
	//    case and underscore/hyphen differences don't matter. This resolves
	//    the name even when the skill directory differs from the skill's
	//    frontmatter name (e.g. tool advertised as "k8s-incident-triage"
	//    living in a directory of a different name).
	index, available := t.buildNameIndex()
	if path, ok := index[normalizeSkillKey(input.Name)]; ok {
		if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path derived from workDir scan, name sanitized above
			return string(data), nil
		}
	}

	// 3) Not found — return the available skill names so the model can
	//    retry with a valid identifier instead of giving up.
	if len(available) > 0 {
		list, _ := json.Marshal(available)
		return fmt.Sprintf(`{"error": "skill %q not found", "available_skills": %s}`, input.Name, list), nil
	}
	return fmt.Sprintf(`{"error": "skill %q not found"}`, input.Name), nil
}

// readByName tries the two on-disk layouts for the given name and its
// underscore->hyphen variant. Returns (contents, true) on the first hit.
func (t *ReadSkillTool) readByName(name string) ([]byte, bool) {
	variants := []string{name}
	if hyphenated := strings.ReplaceAll(name, "_", "-"); hyphenated != name {
		variants = append(variants, hyphenated)
	}
	for _, n := range variants {
		// Flat format: skills/{name}.md
		if data, err := os.ReadFile(filepath.Join(t.workDir, "skills", n+".md")); err == nil { //nolint:gosec // name sanitized by caller
			return data, true
		}
		// Subdirectory format: skills/{name}/SKILL.md
		if data, err := os.ReadFile(filepath.Join(t.workDir, "skills", n, "SKILL.md")); err == nil { //nolint:gosec // name sanitized by caller
			return data, true
		}
	}
	return nil, false
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
