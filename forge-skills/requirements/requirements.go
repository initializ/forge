// Package requirements provides aggregation and derivation of skill requirements.
package requirements

import (
	"sort"

	"github.com/initializ/forge/forge-skills/contract"
)

// AggregateRequirements merges requirements from all entries that have ForgeReqs set.
//
// Promotion rules:
//   - var in both required (skill A) and optional (skill B) → required
//   - var in one_of (skill A) and required (skill B) → stays in required (group still exists)
//   - one_of groups kept separate per skill
func AggregateRequirements(entries []contract.SkillEntry) *contract.AggregatedRequirements {
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

	agg := &contract.AggregatedRequirements{
		Bins:     sortedKeys(binSet),
		EnvOneOf: oneOfGroups,
	}
	agg.EnvRequired = sortedKeys(reqSet)
	agg.EnvOptional = sortedKeys(optSet)
	return agg
}

// AggregateDescriptorRequirements computes the maximum TimeoutHint across descriptors.
func AggregateDescriptorRequirements(descs []contract.SkillDescriptor) int {
	maxTimeout := 0
	for _, d := range descs {
		if d.TimeoutHint > maxTimeout {
			maxTimeout = d.TimeoutHint
		}
	}
	return maxTimeout
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
