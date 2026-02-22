// Package container provides container image building via docker, podman, or buildah.
package container

import "context"

// Builder is the interface for container image builders.
type Builder interface {
	Build(ctx context.Context, opts BuildOptions) (*BuildResult, error)
	Push(ctx context.Context, image string) error
	Available() bool
	Name() string
}

// BuildOptions configures a container image build.
type BuildOptions struct {
	ContextDir string
	Dockerfile string
	Tag        string
	Platform   string
	NoCache    bool
	BuildArgs  map[string]string
}

// BuildResult holds the result of a container image build.
type BuildResult struct {
	ImageID string
	Tag     string
	Size    int64
}

// Detect returns the first available container builder in order: docker, podman, buildah.
// Returns nil if no builder is available.
func Detect() Builder {
	builders := []Builder{
		&DockerBuilder{},
		&PodmanBuilder{},
		&BuildahBuilder{},
	}
	for _, b := range builders {
		if b.Available() {
			return b
		}
	}
	return nil
}

// Get returns a builder by name, or nil if the name is unknown.
func Get(name string) Builder {
	switch name {
	case "docker":
		return &DockerBuilder{}
	case "podman":
		return &PodmanBuilder{}
	case "buildah":
		return &BuildahBuilder{}
	default:
		return nil
	}
}
