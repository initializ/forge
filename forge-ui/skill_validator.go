package forgeui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/parser"
)

var (
	skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	kebabCasePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	egressURLPattern = regexp.MustCompile(`https?://([^/\s"'` + "`" + `]+)`)
)

// validateSkillMD validates SKILL.md content and optional scripts.
//
// editingName, when non-empty, suppresses the "already exists" warning
// for the skill being edited so the operator doesn't see a spurious
// duplicate-name nag every time they Save in the Skill Builder edit
// flow (issue #193). A rename (editor name ≠ editingName) still emits
// the warning because the user IS introducing a new skill with the
// existing one's name and that's the breaking-change case we want to
// flag. Pass "" for the create flow.
func validateSkillMD(content string, scripts map[string]string, agentDir, editingName string) SkillValidationResult {
	var result SkillValidationResult
	result.Valid = true

	if strings.TrimSpace(content) == "" {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:   "skill_md",
			Message: "SKILL.md content is empty",
		})
		return result
	}

	// Parse with metadata using the existing skill parser
	entries, meta, err := parser.ParseWithMetadata(strings.NewReader(content))
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:   "yaml",
			Message: "Failed to parse YAML frontmatter: " + err.Error(),
		})
		return result
	}

	// Check name
	if meta == nil || meta.Name == "" {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:   "name",
			Message: "name is required in frontmatter",
		})
	} else {
		if !skillNamePattern.MatchString(meta.Name) {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:   "name",
				Message: "name must be lowercase kebab-case (e.g. my-skill)",
			})
		}
		if len(meta.Name) > 64 {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:   "name",
				Message: "name must be 64 characters or fewer",
			})
		}
		if strings.Contains(meta.Name, "/") || strings.Contains(meta.Name, "\\") || strings.Contains(meta.Name, "..") {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Field:   "name",
				Message: "name must not contain path separators or '..'",
			})
		}
	}

	// Check description
	if meta == nil || meta.Description == "" {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:   "description",
			Message: "description is required in frontmatter",
		})
	}

	// Warnings

	// Category format
	if meta != nil && meta.Category != "" && !kebabCasePattern.MatchString(meta.Category) {
		result.Warnings = append(result.Warnings, ValidationError{
			Field:   "category",
			Message: "category should be lowercase kebab-case",
		})
	}

	// Body presence
	if len(entries) == 0 {
		result.Warnings = append(result.Warnings, ValidationError{
			Field:   "body",
			Message: "no ## Tool: sections found in the markdown body",
		})
	}

	// Undeclared egress
	if len(scripts) > 0 {
		var declaredEgress []string
		if meta != nil && meta.Metadata != nil {
			declaredEgress = extractDeclaredEgress(meta)
		}
		undeclared := detectUndeclaredEgress(scripts, declaredEgress)
		for _, domain := range undeclared {
			result.Warnings = append(result.Warnings, ValidationError{
				Field:   "egress_domains",
				Message: "scripts reference domain " + domain + " which is not declared in egress_domains",
			})
		}
	}

	// Name uniqueness. Skipped when editingName matches the
	// frontmatter name — that's the "saving over the skill currently
	// being edited" case and the warning would be noise (issue #193).
	if meta != nil && meta.Name != "" && agentDir != "" && meta.Name != editingName {
		skillDir := filepath.Join(agentDir, "skills", meta.Name)
		if _, err := os.Stat(skillDir); err == nil {
			result.Warnings = append(result.Warnings, ValidationError{
				Field:   "name",
				Message: "a skill named '" + meta.Name + "' already exists in this agent",
			})
		}
	}

	return result
}

// extractDeclaredEgress pulls egress_domains from the metadata forge section.
func extractDeclaredEgress(meta *contract.SkillMetadata) []string {
	if meta.Metadata == nil {
		return nil
	}
	forgeMap, ok := meta.Metadata["forge"]
	if !ok || forgeMap == nil {
		return nil
	}
	egressRaw, ok := forgeMap["egress_domains"]
	if !ok || egressRaw == nil {
		return nil
	}
	egressSlice, ok := egressRaw.([]any)
	if !ok {
		return nil
	}
	var domains []string
	for _, v := range egressSlice {
		if s, ok := v.(string); ok {
			domains = append(domains, s)
		}
	}
	return domains
}

// detectUndeclaredEgress scans script contents for HTTP(S) URLs and returns
// domains not found in the declared egress list.
func detectUndeclaredEgress(scripts map[string]string, declaredEgress []string) []string {
	declaredSet := make(map[string]bool, len(declaredEgress))
	for _, d := range declaredEgress {
		declaredSet[strings.TrimPrefix(strings.TrimPrefix(d, "$"), "{")] = true
		declaredSet[d] = true
	}

	foundDomains := make(map[string]bool)
	for _, content := range scripts {
		matches := egressURLPattern.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			if len(m) > 1 {
				domain := m[1]
				// Strip port if present
				if idx := strings.Index(domain, ":"); idx > 0 {
					domain = domain[:idx]
				}
				foundDomains[domain] = true
			}
		}
	}

	var undeclared []string
	for domain := range foundDomains {
		if !declaredSet[domain] {
			undeclared = append(undeclared, domain)
		}
	}
	return undeclared
}

// extractArtifacts parses labeled code fences from an LLM response and
// returns the SKILL.md content and a map of script filename → content.
//
// It is deliberately tolerant of the ways LLMs deviate from the prompt's
// exact quadruple-backtick format, because a missed extraction leaves the
// preview pane empty and the user with no way to save. It accepts:
//   - 3-or-more backticks (not strictly 4), with the closing fence needing
//     at least as many as the opener — so an inner ```json block inside a
//     ````skill.md block doesn't close it early;
//   - a label with surrounding whitespace ("skill.md ", "script: foo.sh");
//   - a block that is unlabeled or language-tagged (```yaml / ```markdown)
//     whose content is clearly a SKILL.md (starts with `---` frontmatter
//     carrying a `name:` key) — used only if no explicit skill.md fence
//     was found.
func extractArtifacts(response string) (skillMD string, scripts map[string]string) {
	scripts = make(map[string]string)

	lines := strings.Split(response, "\n")
	i := 0
	for i < len(lines) {
		openTicks, label, isOpen := fenceOpen(lines[i])
		if !isOpen {
			i++
			continue
		}
		// Find the closing fence: a line of only backticks, at least as
		// many as the opener (so nested lower-count fences are content).
		j := i + 1
		for j < len(lines) && !fenceClose(lines[j], openTicks) {
			j++
		}
		content := strings.Join(lines[i+1:min(j, len(lines))], "\n")
		classifyArtifact(label, content, &skillMD, scripts)
		i = j + 1
	}
	return
}

// fenceOpen reports whether line opens a labeled code fence, returning the
// backtick count and the trimmed label. A bare fence (no label) is not
// treated as an opener so a stray closing fence can't start a phantom block.
func fenceOpen(line string) (ticks int, label string, ok bool) {
	n := 0
	for n < len(line) && line[n] == '`' {
		n++
	}
	if n < 3 {
		return 0, "", false
	}
	label = strings.TrimSpace(line[n:])
	if label == "" {
		return 0, "", false
	}
	return n, label, true
}

// fenceClose reports whether line is a closing fence for an opener with
// openTicks backticks: only backticks (optionally surrounded by space),
// at least openTicks of them.
func fenceClose(line string, openTicks int) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != '`' {
			return false
		}
	}
	return len(t) >= openTicks
}

// classifyArtifact routes a fenced block to skillMD or scripts by its label,
// with a frontmatter-based fallback for unlabeled / language-tagged blocks.
func classifyArtifact(label, content string, skillMD *string, scripts map[string]string) {
	low := strings.ToLower(label)
	switch {
	case low == "skill.md" || low == "skill":
		*skillMD = strings.TrimSpace(content)
	case strings.HasPrefix(low, "script:"):
		filename := strings.TrimSpace(label[len("script:"):])
		if filename != "" {
			scripts[filename] = strings.TrimRight(content, "\n")
		}
	default:
		if *skillMD == "" && looksLikeSkillMD(content) {
			*skillMD = strings.TrimSpace(content)
		}
	}
}

// looksLikeSkillMD reports whether content is plausibly a SKILL.md body:
// a YAML frontmatter block carrying a name key.
func looksLikeSkillMD(content string) bool {
	t := strings.TrimSpace(content)
	if !strings.HasPrefix(t, "---") {
		return false
	}
	return strings.Contains(t, "\nname:")
}
