package packaging

import "github.com/initializ/forge/forge-skills/contract"

// BinManifest aggregates all binary requirements from skills.
type BinManifest struct {
	Requirements []contract.BinRequirement
	SkillOrigin  map[string]string // bin name → skill that declared it
}
