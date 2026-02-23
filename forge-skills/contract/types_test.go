package contract

import "testing"

// Compile-time interface check: verify SkillRegistry is implementable.
var _ SkillRegistry = (*registryStub)(nil)

type registryStub struct{}

func (registryStub) List() ([]SkillDescriptor, error) { return nil, nil }
func (registryStub) Get(string) *SkillDescriptor      { return nil }
func (registryStub) LoadContent(string) ([]byte, error) {
	return nil, nil
}
func (registryStub) HasScript(string) bool             { return false }
func (registryStub) LoadScript(string) ([]byte, error) { return nil, nil }

func TestSkillDescriptorFields(t *testing.T) {
	sd := SkillDescriptor{
		Name:          "github",
		DisplayName:   "GitHub",
		Description:   "Create issues, PRs, and query repositories",
		RequiredEnv:   []string{"GH_TOKEN"},
		RequiredBins:  []string{"gh"},
		EgressDomains: []string{"api.github.com", "github.com"},
	}
	if sd.Name != "github" {
		t.Errorf("Name = %q, want github", sd.Name)
	}
	if len(sd.EgressDomains) != 2 {
		t.Errorf("EgressDomains = %v, want 2 items", sd.EgressDomains)
	}
}

func TestSkillEntryFields(t *testing.T) {
	se := SkillEntry{
		Name:        "web_search",
		Description: "Search the web",
		InputSpec:   "query: string",
		OutputSpec:  "results: []string",
	}
	if se.Name != "web_search" {
		t.Errorf("Name = %q, want web_search", se.Name)
	}
}
