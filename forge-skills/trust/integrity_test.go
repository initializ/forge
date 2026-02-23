package trust

import (
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestComputeChecksum(t *testing.T) {
	content := []byte("hello world")
	cs := ComputeChecksum(content)

	if cs == "" {
		t.Fatal("checksum is empty")
	}
	if len(cs) < len("sha256:") {
		t.Fatalf("checksum too short: %s", cs)
	}
	if cs[:7] != "sha256:" {
		t.Fatalf("checksum prefix wrong: %s", cs)
	}
}

func TestVerifyChecksum(t *testing.T) {
	content := []byte("test content")
	cs := ComputeChecksum(content)

	if !VerifyChecksum(content, cs) {
		t.Fatal("checksum verification failed for matching content")
	}

	if VerifyChecksum([]byte("tampered"), cs) {
		t.Fatal("checksum verification passed for tampered content")
	}
}

func TestChecksumDeterministic(t *testing.T) {
	content := []byte("deterministic test")
	cs1 := ComputeChecksum(content)
	cs2 := ComputeChecksum(content)

	if cs1 != cs2 {
		t.Fatalf("checksum not deterministic: %s vs %s", cs1, cs2)
	}
}

// mockRegistry implements contract.SkillRegistry for testing.
type mockRegistry struct {
	skills  []contract.SkillDescriptor
	content map[string][]byte
	scripts map[string]bool
}

func (m *mockRegistry) List() ([]contract.SkillDescriptor, error) {
	return m.skills, nil
}

func (m *mockRegistry) Get(name string) *contract.SkillDescriptor {
	for i := range m.skills {
		if m.skills[i].Name == name {
			return &m.skills[i]
		}
	}
	return nil
}

func (m *mockRegistry) LoadContent(name string) ([]byte, error) {
	if c, ok := m.content[name]; ok {
		return c, nil
	}
	return nil, nil
}

func (m *mockRegistry) HasScript(name string) bool {
	return m.scripts[name]
}

func (m *mockRegistry) LoadScript(name string) ([]byte, error) {
	return nil, nil
}

func TestGenerateManifest(t *testing.T) {
	reg := &mockRegistry{
		skills: []contract.SkillDescriptor{
			{Name: "github"},
			{Name: "weather"},
		},
		content: map[string][]byte{
			"github":  []byte("# GitHub skill"),
			"weather": []byte("# Weather skill"),
		},
	}

	manifest, err := GenerateManifest(reg)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	if len(manifest.Checksums) != 2 {
		t.Fatalf("expected 2 checksums, got %d", len(manifest.Checksums))
	}

	if manifest.Checksums["github"] == "" {
		t.Fatal("github checksum is empty")
	}
	if manifest.Checksums["weather"] == "" {
		t.Fatal("weather checksum is empty")
	}
}

func TestVerifyManifest_Clean(t *testing.T) {
	reg := &mockRegistry{
		skills: []contract.SkillDescriptor{
			{Name: "github"},
		},
		content: map[string][]byte{
			"github": []byte("# GitHub skill"),
		},
	}

	manifest, _ := GenerateManifest(reg)
	violations := VerifyManifest(reg, manifest)

	if len(violations) != 0 {
		t.Fatalf("expected 0 violations, got %d: %+v", len(violations), violations)
	}
}

func TestVerifyManifest_Tampered(t *testing.T) {
	content := []byte("# GitHub skill")
	reg := &mockRegistry{
		skills:  []contract.SkillDescriptor{{Name: "github"}},
		content: map[string][]byte{"github": content},
	}

	manifest, _ := GenerateManifest(reg)

	// Tamper the content
	reg.content["github"] = []byte("# Tampered skill")

	violations := VerifyManifest(reg, manifest)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].Reason != "mismatch" {
		t.Fatalf("expected mismatch reason, got %q", violations[0].Reason)
	}
}

func TestVerifyManifest_MissingInManifest(t *testing.T) {
	reg := &mockRegistry{
		skills:  []contract.SkillDescriptor{{Name: "github"}, {Name: "new-skill"}},
		content: map[string][]byte{"github": []byte("x"), "new-skill": []byte("y")},
	}

	manifest := &Manifest{
		Version:   "1",
		Checksums: map[string]string{"github": ComputeChecksum([]byte("x"))},
	}

	violations := VerifyManifest(reg, manifest)
	found := false
	for _, v := range violations {
		if v.SkillName == "new-skill" && v.Reason == "missing_in_manifest" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected missing_in_manifest violation for new-skill")
	}
}

func TestVerifyManifest_MissingInRegistry(t *testing.T) {
	reg := &mockRegistry{
		skills:  []contract.SkillDescriptor{{Name: "github"}},
		content: map[string][]byte{"github": []byte("x")},
	}

	manifest := &Manifest{
		Version: "1",
		Checksums: map[string]string{
			"github":  ComputeChecksum([]byte("x")),
			"removed": "sha256:abc",
		},
	}

	violations := VerifyManifest(reg, manifest)
	found := false
	for _, v := range violations {
		if v.SkillName == "removed" && v.Reason == "missing_in_registry" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected missing_in_registry violation for removed skill")
	}
}

func TestMarshalUnmarshalManifest(t *testing.T) {
	manifest := &Manifest{
		Version:   "1",
		Checksums: map[string]string{"github": "sha256:abc123"},
	}

	data, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalManifest failed: %v", err)
	}

	parsed, err := UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("UnmarshalManifest failed: %v", err)
	}

	if parsed.Version != manifest.Version {
		t.Fatalf("version mismatch: %s vs %s", parsed.Version, manifest.Version)
	}
	if parsed.Checksums["github"] != manifest.Checksums["github"] {
		t.Fatal("checksum mismatch after round-trip")
	}
}
