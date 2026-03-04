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
	skillNamePattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	kebabCasePattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	egressURLPattern  = regexp.MustCompile(`https?://([^/\s"'` + "`" + `]+)`)
	artifactFenceExpr = regexp.MustCompile("(?s)````(skill\\.md|script:[^\n]+)\n(.*?)````")
)

// validateSkillMD validates SKILL.md content and optional scripts.
func validateSkillMD(content string, scripts map[string]string, agentDir string) SkillValidationResult {
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

	// Name uniqueness
	if meta != nil && meta.Name != "" && agentDir != "" {
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

// extractArtifacts parses labeled code fences from an LLM response.
// Returns the SKILL.md content and a map of script filename → content.
func extractArtifacts(response string) (skillMD string, scripts map[string]string) {
	scripts = make(map[string]string)

	matches := artifactFenceExpr.FindAllStringSubmatch(response, -1)
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		label := m[1]
		content := m[2]
		if label == "skill.md" {
			skillMD = strings.TrimSpace(content)
		} else if strings.HasPrefix(label, "script:") {
			filename := strings.TrimPrefix(label, "script:")
			scripts[filename] = strings.TrimRight(content, "\n")
		}
	}
	return
}
