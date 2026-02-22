package container

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndReadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image-manifest.json")

	original := &ImageManifest{
		AgentID:  "test-agent",
		Version:  "1.0.0",
		ImageTag: "ghcr.io/org/test-agent:1.0.0",
		Builder:  "docker",
		Platform: "linux/amd64",
		BuiltAt:  "2025-01-01T00:00:00Z",
		BuildDir: "/tmp/build",
		Pushed:   true,
	}

	if err := WriteManifest(path, original); err != nil {
		t.Fatalf("WriteManifest() error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("manifest file not created")
	}

	// Read back
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest() error: %v", err)
	}

	if got.AgentID != original.AgentID {
		t.Errorf("AgentID = %q, want %q", got.AgentID, original.AgentID)
	}
	if got.Version != original.Version {
		t.Errorf("Version = %q, want %q", got.Version, original.Version)
	}
	if got.ImageTag != original.ImageTag {
		t.Errorf("ImageTag = %q, want %q", got.ImageTag, original.ImageTag)
	}
	if got.Builder != original.Builder {
		t.Errorf("Builder = %q, want %q", got.Builder, original.Builder)
	}
	if got.Platform != original.Platform {
		t.Errorf("Platform = %q, want %q", got.Platform, original.Platform)
	}
	if got.BuiltAt != original.BuiltAt {
		t.Errorf("BuiltAt = %q, want %q", got.BuiltAt, original.BuiltAt)
	}
	if got.BuildDir != original.BuildDir {
		t.Errorf("BuildDir = %q, want %q", got.BuildDir, original.BuildDir)
	}
	if got.Pushed != original.Pushed {
		t.Errorf("Pushed = %v, want %v", got.Pushed, original.Pushed)
	}
}

func TestReadManifest_NotFound(t *testing.T) {
	_, err := ReadManifest("/nonexistent/path/manifest.json")
	if err == nil {
		t.Error("expected error for nonexistent manifest")
	}
}

func TestWriteManifest_InvalidPath(t *testing.T) {
	m := &ImageManifest{AgentID: "test"}
	err := WriteManifest("/nonexistent/dir/manifest.json", m)
	if err == nil {
		t.Error("expected error for invalid path")
	}
}
