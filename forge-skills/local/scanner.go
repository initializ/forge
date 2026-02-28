package local

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/parser"
)

// Scan walks top-level directories in fsys looking for SKILL.md files.
// It parses frontmatter to extract SkillDescriptor fields.
// Hidden directories (starting with ".") and "_template/" are skipped.
func Scan(fsys fs.FS) ([]contract.SkillDescriptor, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}

	var skills []contract.SkillDescriptor
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}

		// Check for SKILL.md
		skillPath := name + "/SKILL.md"
		f, err := fsys.Open(skillPath)
		if err != nil {
			continue // no SKILL.md, skip
		}

		// Read raw content for checksum computation
		rawContent, readErr := fs.ReadFile(fsys, skillPath)
		if readErr != nil {
			_ = f.(interface{ Close() error }).Close()
			continue
		}

		// Parse frontmatter
		_, meta, parseErr := parser.ParseWithMetadata(f)
		_ = f.(interface{ Close() error }).Close()
		if parseErr != nil {
			continue
		}

		// Compute checksum
		h := sha256.Sum256(rawContent)
		checksum := fmt.Sprintf("sha256:%x", h)

		sd := contract.SkillDescriptor{
			Name: name,
			Provenance: &contract.Provenance{
				Source:   "local",
				Trust:    contract.TrustLocal,
				Checksum: checksum,
			},
		}

		if meta != nil {
			if meta.Name != "" {
				sd.Name = meta.Name
			}
			if meta.Description != "" {
				sd.Description = meta.Description
			}

			// Extract forge-specific fields
			if meta.Metadata != nil {
				if forgeMap, ok := meta.Metadata["forge"]; ok {
					sd.RequiredBins, sd.RequiredEnv, sd.OneOfEnv, sd.OptionalEnv, sd.EgressDomains, sd.TimeoutHint = extractFromForgeMap(forgeMap)
				}
			}

			// Extract version from frontmatter if present
			if sd.Provenance != nil && meta.Metadata != nil {
				if forgeMap, ok := meta.Metadata["forge"]; ok {
					if v, ok := forgeMap["version"]; ok {
						if vs, ok := v.(string); ok {
							sd.Provenance.Version = vs
						}
					}
				}
			}
		}

		// Derive display name from skill name if not set
		if sd.DisplayName == "" {
			sd.DisplayName = deriveDisplayName(sd.Name)
		}

		skills = append(skills, sd)
	}

	return skills, nil
}

// extractFromForgeMap extracts typed fields from the forge metadata map.
func extractFromForgeMap(forgeMap map[string]any) (bins, reqEnv, oneOfEnv, optEnv, egress []string, timeoutHint int) {
	// Extract egress_domains
	if raw, ok := forgeMap["egress_domains"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					egress = append(egress, s)
				}
			}
		}
	}

	// Extract timeout_hint
	if raw, ok := forgeMap["timeout_hint"]; ok {
		switch v := raw.(type) {
		case int:
			timeoutHint = v
		case float64:
			timeoutHint = int(v)
		}
	}

	// Extract requires
	reqRaw, ok := forgeMap["requires"]
	if !ok {
		return
	}
	reqMap, ok := reqRaw.(map[string]any)
	if !ok {
		return
	}

	// bins
	if binsRaw, ok := reqMap["bins"]; ok {
		if arr, ok := binsRaw.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					bins = append(bins, s)
				}
			}
		}
	}

	// env
	envRaw, ok := reqMap["env"]
	if !ok {
		return
	}
	envMap, ok := envRaw.(map[string]any)
	if !ok {
		return
	}

	reqEnv = extractStringSlice(envMap, "required")
	oneOfEnv = extractStringSlice(envMap, "one_of")
	optEnv = extractStringSlice(envMap, "optional")
	return
}

func extractStringSlice(m map[string]any, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// deriveDisplayName converts a kebab-case skill name to a title-case display name.
func deriveDisplayName(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
