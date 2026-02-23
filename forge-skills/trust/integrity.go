package trust

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-skills/contract"
)

// Manifest stores checksums for all skills in a registry.
type Manifest struct {
	Version   string            `json:"version"`
	Checksums map[string]string `json:"checksums"` // skill name -> "sha256:<hex>"
}

// IntegrityViolation describes a checksum mismatch or missing entry.
type IntegrityViolation struct {
	SkillName string `json:"skill_name"`
	Expected  string `json:"expected"`
	Actual    string `json:"actual"`
	Reason    string `json:"reason"` // "mismatch", "missing_in_manifest", "missing_in_registry"
}

// ComputeChecksum returns the SHA-256 checksum of content as "sha256:<hex>".
func ComputeChecksum(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", h)
}

// VerifyChecksum checks whether content matches the expected checksum string.
func VerifyChecksum(content []byte, expected string) bool {
	return ComputeChecksum(content) == expected
}

// GenerateManifest creates a Manifest from all skills in the registry.
func GenerateManifest(registry contract.SkillRegistry) (*Manifest, error) {
	skills, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}

	m := &Manifest{
		Version:   "1",
		Checksums: make(map[string]string, len(skills)),
	}

	for _, sd := range skills {
		content, err := registry.LoadContent(sd.Name)
		if err != nil {
			return nil, fmt.Errorf("loading content for %q: %w", sd.Name, err)
		}
		m.Checksums[sd.Name] = ComputeChecksum(content)
	}

	return m, nil
}

// VerifyManifest checks all skills in the registry against the manifest.
// It returns a list of violations (empty if everything matches).
func VerifyManifest(registry contract.SkillRegistry, manifest *Manifest) []IntegrityViolation {
	var violations []IntegrityViolation

	skills, err := registry.List()
	if err != nil {
		return []IntegrityViolation{{Reason: "registry_error"}}
	}

	registryNames := make(map[string]bool, len(skills))
	for _, sd := range skills {
		registryNames[sd.Name] = true

		expected, inManifest := manifest.Checksums[sd.Name]
		if !inManifest {
			violations = append(violations, IntegrityViolation{
				SkillName: sd.Name,
				Reason:    "missing_in_manifest",
			})
			continue
		}

		content, err := registry.LoadContent(sd.Name)
		if err != nil {
			violations = append(violations, IntegrityViolation{
				SkillName: sd.Name,
				Expected:  expected,
				Reason:    "content_unavailable",
			})
			continue
		}

		actual := ComputeChecksum(content)
		if actual != expected {
			violations = append(violations, IntegrityViolation{
				SkillName: sd.Name,
				Expected:  expected,
				Actual:    actual,
				Reason:    "mismatch",
			})
		}
	}

	// Check for entries in manifest not in registry
	for name := range manifest.Checksums {
		if !registryNames[name] {
			violations = append(violations, IntegrityViolation{
				SkillName: name,
				Expected:  manifest.Checksums[name],
				Reason:    "missing_in_registry",
			})
		}
	}

	return violations
}

// MarshalManifest serializes a manifest to JSON.
func MarshalManifest(m *Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// UnmarshalManifest deserializes a manifest from JSON.
func UnmarshalManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
