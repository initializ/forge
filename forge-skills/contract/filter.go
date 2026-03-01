package contract

import "strings"

// FilterSkills returns the subset of skills matching the given filter.
// Category is matched case-insensitively (exact match).
// Tags use AND semantics: a skill must have ALL listed tags to match.
// Empty filter fields match all skills.
func FilterSkills(skills []SkillDescriptor, f SkillFilter) []SkillDescriptor {
	if f.Category == "" && len(f.Tags) == 0 {
		return skills
	}

	var result []SkillDescriptor
	for _, s := range skills {
		if f.Category != "" && !strings.EqualFold(s.Category, f.Category) {
			continue
		}
		if !hasAllTags(s.Tags, f.Tags) {
			continue
		}
		result = append(result, s)
	}
	return result
}

// hasAllTags returns true if skillTags contains every tag in required.
func hasAllTags(skillTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]bool, len(skillTags))
	for _, t := range skillTags {
		set[strings.ToLower(t)] = true
	}
	for _, t := range required {
		if !set[strings.ToLower(t)] {
			return false
		}
	}
	return true
}
