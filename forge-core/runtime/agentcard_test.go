package runtime

import (
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

// Regression tests for issue #85 / FWS-1 — A2A 0.3.0 Agent Card
// conformance. These pin the JSON shape and the SKILL.md → AgentSkill
// mapping that downstream A2A clients (initializ platform, peer
// agents) will consume.

func TestAgentCardFromConfig_RequiredFieldsPopulated(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID: "test-agent",
		Version: "0.4.2",
	}
	card := AgentCardFromConfig(cfg, "http://localhost:8080")

	// A2A 0.3.0 requires these fields on every card.
	if card.Name != "test-agent" {
		t.Errorf("Name = %q, want %q", card.Name, "test-agent")
	}
	if card.URL != "http://localhost:8080" {
		t.Errorf("URL = %q, want base URL", card.URL)
	}
	if card.Version != "0.4.2" {
		t.Errorf("Version = %q, want forge.yaml version", card.Version)
	}
	if card.ProtocolVersion != "0.3.0" {
		t.Errorf("ProtocolVersion = %q, want pinned 0.3.0", card.ProtocolVersion)
	}
	if len(card.DefaultInputModes) == 0 {
		t.Errorf("DefaultInputModes must be non-empty per A2A 0.3.0")
	}
	if len(card.DefaultOutputModes) == 0 {
		t.Errorf("DefaultOutputModes must be non-empty per A2A 0.3.0")
	}
}

func TestAgentCardFromConfig_ZeroValuesDefault(t *testing.T) {
	// Pre-#85 forge.yaml files might not carry version. The card
	// builder must still produce a spec-conformant non-empty version.
	cfg := &types.ForgeConfig{AgentID: "noversion-agent"}
	card := AgentCardFromConfig(cfg, "http://localhost:8080")

	if card.Version == "" {
		t.Errorf("Version must default to non-empty for spec conformance")
	}
}

func TestAgentCardFromSpec_PreservesSpecVersion(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID:     "spec-agent",
		Name:        "Spec Agent",
		Description: "Built from a spec",
		Version:     "1.2.3",
	}
	card := AgentCardFromSpec(spec, "http://example.com")

	if card.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", card.Version, "1.2.3")
	}
	if card.Name != "Spec Agent" {
		t.Errorf("Name = %q, want spec.Name", card.Name)
	}
	if card.ProtocolVersion != "0.3.0" {
		t.Errorf("ProtocolVersion = %q, want pinned 0.3.0", card.ProtocolVersion)
	}
}

func TestAgentCardFromSpec_FallsBackToAgentIDWhenNameEmpty(t *testing.T) {
	spec := &agentspec.AgentSpec{AgentID: "fallback-agent"}
	card := AgentCardFromSpec(spec, "http://example.com")
	if card.Name != "fallback-agent" {
		t.Errorf("Name = %q, want %q (fallback to AgentID)", card.Name, "fallback-agent")
	}
}

func TestAgentCardFromSpec_SkillsCarryRequiredTags(t *testing.T) {
	// A2A 0.3.0 requires Skill.Tags to be non-empty. The card builder
	// must fill the field even when the spec doesn't supply tags.
	spec := &agentspec.AgentSpec{
		AgentID: "agent",
		A2A: &agentspec.A2AConfig{
			Skills: []agentspec.A2ASkill{
				{ID: "tagged", Name: "Tagged", Description: "has tags", Tags: []string{"a", "b"}},
				{ID: "untagged", Name: "Untagged", Description: "no tags"},
			},
		},
	}
	card := AgentCardFromSpec(spec, "http://example.com")

	for _, s := range card.Skills {
		if len(s.Tags) == 0 {
			t.Errorf("skill %q has empty Tags; A2A 0.3.0 requires non-empty", s.ID)
		}
	}
}

func TestSkillFromDescriptor_MapsSKILLmdFrontmatterFields(t *testing.T) {
	// SKILL.md frontmatter -> A2A AgentSkill. Pinned per issue #85
	// "no information loss in either direction" for spec-defined fields.
	d := contract.SkillDescriptor{
		Name:        "weather",
		DisplayName: "Weather Lookup",
		Description: "Fetch the current weather for a city.",
		Category:    "info-retrieval",
		Tags:        []string{"web", "json"},
	}
	s := skillFromDescriptor(d)

	if s.ID != "weather" {
		t.Errorf("ID = %q, want %q", s.ID, "weather")
	}
	if s.Name != "Weather Lookup" {
		t.Errorf("Name = %q, want DisplayName", s.Name)
	}
	if s.Description != d.Description {
		t.Errorf("Description = %q, want %q", s.Description, d.Description)
	}
	// Tags: category first, then frontmatter tags, deduped.
	if len(s.Tags) != 3 || s.Tags[0] != "info-retrieval" {
		t.Errorf("Tags = %v, want category-first then frontmatter tags", s.Tags)
	}
}

func TestSkillFromDescriptor_DefaultsTagsWhenEmpty(t *testing.T) {
	s := skillFromDescriptor(contract.SkillDescriptor{Name: "bare"})
	if len(s.Tags) == 0 {
		t.Errorf("Tags must default to non-empty for A2A 0.3.0 conformance")
	}
}

func TestSkillFromDescriptor_NameFallsBackToID(t *testing.T) {
	s := skillFromDescriptor(contract.SkillDescriptor{Name: "noDisplay"})
	if s.Name != "noDisplay" {
		t.Errorf("Name = %q, want fallback to ID when DisplayName empty", s.Name)
	}
}

func TestAppendSkillsFromDescriptors_DeterministicOrdering(t *testing.T) {
	// The /.well-known/agent-card.json bytes must be stable across
	// restarts so consumers can hash for change detection. Skills
	// always sort by ID after this helper runs.
	card := &a2a.AgentCard{}
	AppendSkillsFromDescriptors(card, []contract.SkillDescriptor{
		{Name: "zoo"},
		{Name: "alpha"},
		{Name: "mango"},
	})
	got := []string{card.Skills[0].ID, card.Skills[1].ID, card.Skills[2].ID}
	want := []string{"alpha", "mango", "zoo"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Skills order %v, want %v", got, want)
			break
		}
	}
}

func TestAppendSkillsFromDescriptors_DedupesAgainstExistingSkills(t *testing.T) {
	card := &a2a.AgentCard{
		Skills: []a2a.Skill{{ID: "existing", Name: "Existing", Tags: []string{"x"}}},
	}
	AppendSkillsFromDescriptors(card, []contract.SkillDescriptor{
		{Name: "existing", Description: "duplicate, should be skipped"},
		{Name: "new", Description: "added"},
	})

	if len(card.Skills) != 2 {
		t.Fatalf("expected 2 skills (existing + new), got %d", len(card.Skills))
	}
	// "existing" entry was preserved verbatim — not overwritten.
	for _, s := range card.Skills {
		if s.ID == "existing" && s.Description != "" {
			t.Errorf("existing skill was clobbered: %+v", s)
		}
	}
}

func TestAgentCardJSON_OmitsNilFields(t *testing.T) {
	// A2A 0.3.0 prefers omission of optional fields over null. The
	// type's json tags carry omitempty for the right ones.
	cfg := &types.ForgeConfig{AgentID: "min", Version: "0.1.0"}
	card := AgentCardFromConfig(cfg, "http://localhost:8080")

	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(raw)

	// Should NOT contain capabilities (nil pointer) or securitySchemes
	// (nil map). Confirms omitempty is wired right.
	for _, forbidden := range []string{`"capabilities"`, `"securitySchemes"`, `"provider"`, `"documentationUrl"`, `"iconUrl"`} {
		if containsField(js, forbidden) {
			t.Errorf("nil/empty optional field %s should be omitted, got:\n%s", forbidden, js)
		}
	}
	// Should contain required ones.
	for _, required := range []string{`"name"`, `"url"`, `"version"`, `"protocolVersion"`, `"defaultInputModes"`, `"defaultOutputModes"`} {
		if !containsField(js, required) {
			t.Errorf("required field %s missing from JSON:\n%s", required, js)
		}
	}
}

// containsField checks whether the JSON contains a top-level key.
// Avoids false positives when the field name appears inside a value.
func containsField(js, field string) bool {
	// Cheap: the marshaled card is single-line; the field always appears
	// after `{` or `,` adjacent to no quote. Sufficient for these tests.
	return jsonHas(js, field)
}

func jsonHas(js, key string) bool {
	for i := 0; i+len(key) <= len(js); i++ {
		if js[i:i+len(key)] == key {
			// Reject when preceded by `:` (means it's a value)
			if i > 0 && (js[i-1] == ':') {
				continue
			}
			return true
		}
	}
	return false
}
