package skills

import "sort"

// AggregatedRequirements is the union of all skill requirements.
type AggregatedRequirements struct {
	Bins        []string   // union of all bins, deduplicated, sorted
	EnvRequired []string   // union of required vars (promoted from optional if needed)
	EnvOneOf    [][]string // separate groups per skill (not merged across skills)
	EnvOptional []string   // union of optional vars minus those promoted to required
}

// AggregateRequirements merges requirements from all entries that have ForgeReqs set.
//
// Promotion rules:
//   - var in both required (skill A) and optional (skill B) → required
//   - var in one_of (skill A) and required (skill B) → stays in required (group still exists)
//   - one_of groups kept separate per skill
func AggregateRequirements(entries []SkillEntry) *AggregatedRequirements {
	binSet := make(map[string]bool)
	reqSet := make(map[string]bool)
	optSet := make(map[string]bool)
	var oneOfGroups [][]string

	for _, e := range entries {
		if e.ForgeReqs == nil {
			continue
		}
		for _, b := range e.ForgeReqs.Bins {
			binSet[b] = true
		}
		if e.ForgeReqs.Env != nil {
			for _, v := range e.ForgeReqs.Env.Required {
				reqSet[v] = true
			}
			if len(e.ForgeReqs.Env.OneOf) > 0 {
				oneOfGroups = append(oneOfGroups, e.ForgeReqs.Env.OneOf)
			}
			for _, v := range e.ForgeReqs.Env.Optional {
				optSet[v] = true
			}
		}
	}

	// Promotion: optional vars that appear in required get promoted
	for v := range optSet {
		if reqSet[v] {
			delete(optSet, v)
		}
	}

	agg := &AggregatedRequirements{
		Bins:     sortedKeys(binSet),
		EnvOneOf: oneOfGroups,
	}
	agg.EnvRequired = sortedKeys(reqSet)
	agg.EnvOptional = sortedKeys(optSet)
	return agg
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
