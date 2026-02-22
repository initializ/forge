package container

import (
	"testing"
)

func TestDockerBuilder_Name(t *testing.T) {
	b := &DockerBuilder{}
	if b.Name() != "docker" {
		t.Errorf("Name() = %q, want %q", b.Name(), "docker")
	}
}

func TestPodmanBuilder_Name(t *testing.T) {
	b := &PodmanBuilder{}
	if b.Name() != "podman" {
		t.Errorf("Name() = %q, want %q", b.Name(), "podman")
	}
}

func TestBuildahBuilder_Name(t *testing.T) {
	b := &BuildahBuilder{}
	if b.Name() != "buildah" {
		t.Errorf("Name() = %q, want %q", b.Name(), "buildah")
	}
}

func TestParseImageID(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "docker style",
			output: "Step 1/5 : FROM alpine\nSuccessfully built abc123def",
			want:   "abc123def",
		},
		{
			name:   "sha256 hash",
			output: "Step 1/5 : FROM alpine\nsha256:abc123def456",
			want:   "sha256:abc123def456",
		},
		{
			name:   "last line fallback",
			output: "some-image-id",
			want:   "some-image-id",
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseImageID(tt.output)
			if got != tt.want {
				t.Errorf("parseImageID() = %q, want %q", got, tt.want)
			}
		})
	}
}
