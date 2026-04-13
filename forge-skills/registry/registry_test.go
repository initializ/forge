package registry

import (
	"testing"
)

func TestDefault_Loads(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}
	if reg == nil {
		t.Fatal("Default() returned nil")
	}
	if len(reg.entries) == 0 {
		t.Fatal("registry has no entries")
	}
}

func TestLookup_Known(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}

	tests := []struct {
		name    string
		wantApt string
	}{
		{"jq", "jq"},
		{"curl", "curl"},
		{"psql", "postgresql-client"},
	}

	for _, tt := range tests {
		e, ok := reg.Lookup(tt.name)
		if !ok {
			t.Errorf("Lookup(%q) not found", tt.name)
			continue
		}
		if e.Apt != tt.wantApt {
			t.Errorf("Lookup(%q).Apt = %q, want %q", tt.name, e.Apt, tt.wantApt)
		}
	}
}

func TestLookup_Unknown(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}
	_, ok := reg.Lookup("nonexistent-binary-xyz")
	if ok {
		t.Error("expected Lookup for unknown binary to return false")
	}
}

func TestLookup_Heavy(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}
	e, ok := reg.Lookup("playwright")
	if !ok {
		t.Fatal("playwright not found in registry")
	}
	if !e.Heavy {
		t.Error("playwright should be marked as heavy")
	}
	if !e.RequiresUbuntu {
		t.Error("playwright should require ubuntu")
	}
	if e.Image == "" {
		t.Error("playwright should have an image template")
	}
}

func TestExpandTemplate(t *testing.T) {
	tests := []struct {
		tmpl    string
		version string
		want    string
	}{
		{"https://example.com/v{{.Version}}/bin", "1.2.3", "https://example.com/v1.2.3/bin"},
		{"no-template-here", "1.0", "no-template-here"},
		{"{{.Version}}-{{.Version}}", "2.0", "2.0-2.0"},
	}

	for _, tt := range tests {
		got, err := ExpandTemplate(tt.tmpl, tt.version)
		if err != nil {
			t.Errorf("ExpandTemplate(%q, %q) error: %v", tt.tmpl, tt.version, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ExpandTemplate(%q, %q) = %q, want %q", tt.tmpl, tt.version, got, tt.want)
		}
	}
}

func TestResolveVersion(t *testing.T) {
	e := RegistryEntry{DefaultVersion: "1.0.0"}
	if v := e.ResolveVersion("2.0.0"); v != "2.0.0" {
		t.Errorf("explicit version: got %q, want 2.0.0", v)
	}
	if v := e.ResolveVersion(""); v != "1.0.0" {
		t.Errorf("default version: got %q, want 1.0.0", v)
	}
}

func TestLookup_RequiresFirst(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}
	e, ok := reg.Lookup("aws")
	if !ok {
		t.Fatal("aws not found")
	}
	if len(e.RequiresFirst) == 0 {
		t.Error("aws should have requires_first dependencies")
	}
	found := false
	for _, dep := range e.RequiresFirst {
		if dep == "unzip" {
			found = true
		}
	}
	if !found {
		t.Errorf("aws requires_first should include unzip, got %v", e.RequiresFirst)
	}
}
