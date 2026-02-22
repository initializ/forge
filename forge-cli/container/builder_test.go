package container

import (
	"testing"
)

func TestGet_KnownBuilders(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"docker", "docker"},
		{"podman", "podman"},
		{"buildah", "buildah"},
	}

	for _, tt := range tests {
		b := Get(tt.name)
		if b == nil {
			t.Errorf("Get(%q) returned nil", tt.name)
			continue
		}
		if b.Name() != tt.expected {
			t.Errorf("Get(%q).Name() = %q, want %q", tt.name, b.Name(), tt.expected)
		}
	}
}

func TestGet_UnknownBuilder(t *testing.T) {
	b := Get("unknown")
	if b != nil {
		t.Errorf("Get(\"unknown\") = %v, want nil", b)
	}
}

func TestDetect_ReturnsBuilderOrNil(t *testing.T) {
	// Detect should return a builder or nil depending on what's installed.
	// We can't assert which builder is available in CI, but we can verify
	// it doesn't panic and returns a valid type.
	b := Detect()
	if b != nil {
		name := b.Name()
		if name != "docker" && name != "podman" && name != "buildah" {
			t.Errorf("Detect() returned builder with unexpected name: %q", name)
		}
	}
}

func TestBuildOptions_Defaults(t *testing.T) {
	opts := BuildOptions{}
	if opts.ContextDir != "" {
		t.Errorf("default ContextDir = %q, want empty", opts.ContextDir)
	}
	if opts.NoCache {
		t.Error("default NoCache = true, want false")
	}
}
