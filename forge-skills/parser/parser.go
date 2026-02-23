// Package parser provides a reusable parser for skills.md files.
package parser

import (
	"bufio"
	"bytes"
	"io"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
	"gopkg.in/yaml.v3"
)

// Parse reads skill entries from an io.Reader and extracts structured SkillEntry values.
//
// Supported formats:
//   - "## Tool: <name>" heading starts a new entry; paragraph lines become Description
//   - "**Input:** <text>" sets InputSpec on the current entry
//   - "**Output:** <text>" sets OutputSpec on the current entry
//   - "- <name>" (single-word/hyphenated list item) creates an entry with Name only (legacy)
func Parse(r io.Reader) ([]contract.SkillEntry, error) {
	var entries []contract.SkillEntry
	var current *contract.SkillEntry

	finalize := func() {
		if current != nil {
			current.Description = strings.TrimSpace(current.Description)
			entries = append(entries, *current)
			current = nil
		}
	}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// "## Tool: <name>" heading
		if strings.HasPrefix(trimmed, "## Tool:") {
			finalize()
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "## Tool:"))
			if name != "" {
				current = &contract.SkillEntry{Name: name}
			}
			continue
		}

		// Another heading terminates current entry
		if strings.HasPrefix(trimmed, "#") {
			finalize()
			continue
		}

		// Inside a tool entry
		if current != nil {
			if strings.HasPrefix(trimmed, "**Input:**") {
				current.InputSpec = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Input:**"))
				continue
			}
			if strings.HasPrefix(trimmed, "**Output:**") {
				current.OutputSpec = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Output:**"))
				continue
			}
			// Paragraph text becomes description
			if trimmed != "" {
				if current.Description != "" {
					current.Description += " "
				}
				current.Description += trimmed
			}
			continue
		}

		// Legacy: "- <name>" list items (single-word, no spaces, max 64 chars)
		if strings.HasPrefix(trimmed, "- ") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if name != "" && !strings.Contains(name, " ") && len(name) <= 64 {
				entries = append(entries, contract.SkillEntry{Name: name})
			}
		}
	}

	finalize()
	return entries, scanner.Err()
}

// ParseWithMetadata extracts optional YAML frontmatter (between --- delimiters)
// then passes the markdown body through existing Parse(). Returns entries with
// metadata attached, plus the top-level SkillMetadata.
func ParseWithMetadata(r io.Reader) ([]contract.SkillEntry, *contract.SkillMetadata, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}

	fm, body, hasFM := extractFrontmatter(content)

	var meta *contract.SkillMetadata
	if hasFM {
		meta = &contract.SkillMetadata{}
		if err := yaml.Unmarshal(fm, meta); err != nil {
			return nil, nil, err
		}
	}

	var forgeReqs *contract.SkillRequirements
	var egressDomains []string
	if meta != nil {
		forgeReqs, egressDomains = extractForgeReqs(meta)
	}

	entries, err := Parse(bytes.NewReader(body))
	if err != nil {
		return nil, meta, err
	}

	// Attach metadata to each entry
	for i := range entries {
		entries[i].Metadata = meta
		entries[i].ForgeReqs = forgeReqs
		_ = egressDomains // egress_domains are available via ForgeSkillMeta but not stored on SkillEntry
	}

	return entries, meta, nil
}

// extractFrontmatter splits content at --- delimiters.
// Returns (frontmatter, body, hasFrontmatter).
func extractFrontmatter(content []byte) ([]byte, []byte, bool) {
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, content, false
	}

	// Find the opening ---
	start := bytes.Index(trimmed, []byte("---"))
	afterOpen := start + 3

	// Skip to the next line
	nlIdx := bytes.IndexByte(trimmed[afterOpen:], '\n')
	if nlIdx < 0 {
		return nil, content, false
	}
	fmStart := afterOpen + nlIdx + 1

	// Find closing ---
	rest := trimmed[fmStart:]
	closeIdx := -1
	scanner := bufio.NewScanner(bytes.NewReader(rest))
	pos := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			closeIdx = pos
			break
		}
		pos += len(line) + 1 // +1 for \n
	}

	if closeIdx < 0 {
		return nil, content, false
	}

	fm := rest[:closeIdx]
	body := rest[closeIdx+3:] // skip past "---"
	// Trim leading newline from body
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	}

	return fm, body, true
}

// extractForgeReqs extracts SkillRequirements and egress_domains from the generic metadata map
// by re-marshaling metadata["forge"] through yaml round-trip into ForgeSkillMeta.
func extractForgeReqs(meta *contract.SkillMetadata) (*contract.SkillRequirements, []string) {
	if meta == nil || meta.Metadata == nil {
		return nil, nil
	}
	forgeMap, ok := meta.Metadata["forge"]
	if !ok || forgeMap == nil {
		return nil, nil
	}

	// Re-marshal the forge map to YAML, then unmarshal into ForgeSkillMeta
	data, err := yaml.Marshal(forgeMap)
	if err != nil {
		return nil, nil
	}

	var forgeMeta contract.ForgeSkillMeta
	if err := yaml.Unmarshal(data, &forgeMeta); err != nil {
		return nil, nil
	}

	return forgeMeta.Requires, forgeMeta.EgressDomains
}
