package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/parser"
	forgeui "github.com/initializ/forge/forge-ui"
)

// SkillRequirementsInfo holds parsed requirements from a SKILL.md.
type SkillRequirementsInfo struct {
	EnvReqs       *contract.EnvRequirements
	EgressDomains []string
}

// ParseSkillRequirements parses a SKILL.md string and extracts env requirements
// and egress domains from its frontmatter metadata.
func ParseSkillRequirements(skillMD string) *SkillRequirementsInfo {
	entries, meta, err := parser.ParseWithMetadata(strings.NewReader(skillMD))
	if err != nil || meta == nil {
		return &SkillRequirementsInfo{}
	}

	reqs, egressDomains, _ := parser.ExtractForgeReqs(meta)

	info := &SkillRequirementsInfo{
		EgressDomains: egressDomains,
	}

	if reqs != nil && reqs.Env != nil {
		info.EnvReqs = reqs.Env
	}

	_ = entries // entries not needed here
	return info
}

// MergeEgressDomains reads forge.yaml, adds new domains to the egress
// allowed_domains list (dedup + sort), and writes back. Returns the list
// of newly added domains.
func MergeEgressDomains(agentDir string, domains []string) ([]string, error) {
	if len(domains) == 0 {
		return nil, nil
	}

	yamlPath := filepath.Join(agentDir, "forge.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("reading forge.yaml: %w", err)
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Find existing allowed_domains and collect them
	existing := make(map[string]bool)
	allowedIdx := -1    // line index of "allowed_domains:"
	lastDomainIdx := -1 // line index of last "    - domain" entry
	indent := ""

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "allowed_domains:") {
			allowedIdx = i
			// Detect indentation of the line containing allowed_domains
			indent = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			continue
		}
		if allowedIdx >= 0 && i > allowedIdx {
			// We're inside the allowed_domains block
			if strings.HasPrefix(trimmed, "- ") {
				domain := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				existing[domain] = true
				lastDomainIdx = i
			} else if trimmed == "" {
				// empty line, continue scanning
				continue
			} else {
				// Hit a non-list line → end of allowed_domains block
				break
			}
		}
	}

	// Determine which domains are new
	var added []string
	for _, d := range domains {
		if !existing[d] {
			added = append(added, d)
		}
	}

	if len(added) == 0 {
		return nil, nil
	}
	sort.Strings(added)

	if allowedIdx >= 0 {
		// Insert new domains after the last existing domain entry
		insertIdx := lastDomainIdx + 1
		if lastDomainIdx < allowedIdx {
			// allowed_domains: exists but has no entries yet
			insertIdx = allowedIdx + 1
		}
		// Detect the list item indentation from existing entries
		itemIndent := indent + "    "
		if lastDomainIdx > allowedIdx {
			existingLine := lines[lastDomainIdx]
			itemIndent = existingLine[:len(existingLine)-len(strings.TrimLeft(existingLine, " \t"))]
		}

		var newLines []string
		for _, d := range added {
			newLines = append(newLines, itemIndent+"- "+d)
		}

		// Splice into the lines array
		result := make([]string, 0, len(lines)+len(newLines))
		result = append(result, lines[:insertIdx]...)
		result = append(result, newLines...)
		result = append(result, lines[insertIdx:]...)
		lines = result
	} else {
		// No egress section with allowed_domains found — append one.
		// Check if there's an "egress:" section
		egressIdx := -1
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "egress:" || strings.HasPrefix(trimmed, "egress:") {
				egressIdx = i
				break
			}
		}

		if egressIdx >= 0 {
			// Find where to insert allowed_domains under egress
			insertIdx := egressIdx + 1
			// Skip existing egress sub-keys
			egressIndent := lines[egressIdx][:len(lines[egressIdx])-len(strings.TrimLeft(lines[egressIdx], " \t"))]
			subIndent := egressIndent + "  "
			for insertIdx < len(lines) {
				trimmed := strings.TrimSpace(lines[insertIdx])
				if trimmed == "" {
					insertIdx++
					continue
				}
				lineIndent := lines[insertIdx][:len(lines[insertIdx])-len(strings.TrimLeft(lines[insertIdx], " \t"))]
				if len(lineIndent) <= len(egressIndent) && trimmed != "" {
					break
				}
				insertIdx++
			}
			var newLines []string
			newLines = append(newLines, subIndent+"allowed_domains:")
			for _, d := range added {
				newLines = append(newLines, subIndent+"  - "+d)
			}

			result := make([]string, 0, len(lines)+len(newLines))
			result = append(result, lines[:insertIdx]...)
			result = append(result, newLines...)
			result = append(result, lines[insertIdx:]...)
			lines = result
		} else {
			// No egress section at all — append both egress and allowed_domains
			lines = append(lines, "egress:")
			lines = append(lines, "  allowed_domains:")
			for _, d := range added {
				lines = append(lines, "    - "+d)
			}
		}
	}

	if err := os.WriteFile(yamlPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return nil, fmt.Errorf("writing forge.yaml: %w", err)
	}

	return added, nil
}

// AppendEnvVars appends key=value pairs to .env, skipping keys already present.
// Returns the list of keys that were actually written.
func AppendEnvVars(agentDir string, vars map[string]string, skillName string) ([]string, error) {
	if len(vars) == 0 {
		return nil, nil
	}

	envPath := filepath.Join(agentDir, ".env")
	existing, _ := runtime.LoadEnvFile(envPath)

	// Also check for encrypted secret placeholders
	secretKeys := loadSecretPlaceholders(envPath)

	var toWrite []string
	for k := range vars {
		if existing[k] != "" || secretKeys[k] {
			continue
		}
		toWrite = append(toWrite, k)
	}

	if len(toWrite) == 0 {
		return nil, nil
	}
	sort.Strings(toWrite)

	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening .env: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Check if we need a leading newline
	if info, _ := f.Stat(); info != nil && info.Size() > 0 {
		// Read last byte to check if file ends with newline
		rf, _ := os.Open(envPath)
		if rf != nil {
			buf := make([]byte, 1)
			if n, _ := rf.ReadAt(buf, info.Size()-1); n == 1 && buf[0] != '\n' {
				_, _ = fmt.Fprintln(f)
			}
			_ = rf.Close()
		}
	}

	_, _ = fmt.Fprintf(f, "# Required by %s skill\n", skillName)
	var written []string
	for _, k := range toWrite {
		_, _ = fmt.Fprintf(f, "%s=%s\n", k, vars[k])
		written = append(written, k)
	}

	return written, nil
}

// CheckMissingEnv checks OS env + .env + secret placeholders and returns
// entries for env vars that are still missing.
func CheckMissingEnv(agentDir string, envReqs *contract.EnvRequirements) []forgeui.SkillEnvEntry {
	if envReqs == nil {
		return nil
	}

	envPath := filepath.Join(agentDir, ".env")
	dotEnv, _ := runtime.LoadEnvFile(envPath)
	secretKeys := loadSecretPlaceholders(envPath)

	isSet := func(key string) bool {
		if os.Getenv(key) != "" {
			return true
		}
		if dotEnv[key] != "" {
			return true
		}
		return secretKeys[key]
	}

	var missing []forgeui.SkillEnvEntry

	for _, env := range envReqs.Required {
		if !isSet(env) {
			missing = append(missing, forgeui.SkillEnvEntry{Name: env, Kind: "required"})
		}
	}

	// For one_of groups, check if at least one is set
	if len(envReqs.OneOf) > 0 {
		hasOne := false
		for _, env := range envReqs.OneOf {
			if isSet(env) {
				hasOne = true
				break
			}
		}
		if !hasOne {
			for _, env := range envReqs.OneOf {
				missing = append(missing, forgeui.SkillEnvEntry{Name: env, Kind: "one_of"})
			}
		}
	}

	for _, env := range envReqs.Optional {
		if !isSet(env) {
			missing = append(missing, forgeui.SkillEnvEntry{Name: env, Kind: "optional"})
		}
	}

	return missing
}
