package contract

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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
func (registryStub) ListScripts(string) []string       { return nil }

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

func TestSkillRequirements_CapabilitiesYAMLRoundTrip(t *testing.T) {
	in := `
bins:
  - jq
capabilities:
  - browser
`
	var reqs SkillRequirements
	if err := yaml.Unmarshal([]byte(in), &reqs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(reqs.Capabilities) != 1 || reqs.Capabilities[0] != CapabilityBrowser {
		t.Errorf("Capabilities = %v, want [browser]", reqs.Capabilities)
	}
}

func TestTrustHints_ExplicitFalseVsAbsent(t *testing.T) {
	// Explicit network: false must be distinguishable from an undeclared hint:
	// only the explicit false contradicts a network-requiring capability.
	explicit := `
trust_hints:
  network: false
  filesystem: read
  shell: true
`
	var meta ForgeSkillMeta
	if err := yaml.Unmarshal([]byte(explicit), &meta); err != nil {
		t.Fatalf("unmarshal explicit: %v", err)
	}
	if meta.TrustHints == nil || meta.TrustHints.Network == nil {
		t.Fatal("explicit network hint parsed as absent")
	}
	if *meta.TrustHints.Network {
		t.Error("network = true, want false")
	}
	if meta.TrustHints.Filesystem != "read" {
		t.Errorf("filesystem = %q, want %q", meta.TrustHints.Filesystem, "read")
	}
	if meta.TrustHints.Shell == nil || !*meta.TrustHints.Shell {
		t.Error("shell hint not parsed as explicit true")
	}

	absent := `
trust_hints:
  filesystem: read
`
	var meta2 ForgeSkillMeta
	if err := yaml.Unmarshal([]byte(absent), &meta2); err != nil {
		t.Fatalf("unmarshal absent: %v", err)
	}
	if meta2.TrustHints == nil {
		t.Fatal("trust_hints block parsed as nil")
	}
	if meta2.TrustHints.Network != nil {
		t.Errorf("undeclared network hint parsed as %v, want nil", *meta2.TrustHints.Network)
	}
}
