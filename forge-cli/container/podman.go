package container

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PodmanBuilder builds container images using the podman CLI.
type PodmanBuilder struct{}

func (b *PodmanBuilder) Name() string { return "podman" }

func (b *PodmanBuilder) Available() bool {
	return exec.Command("podman", "info").Run() == nil
}

func (b *PodmanBuilder) Build(ctx context.Context, opts BuildOptions) (*BuildResult, error) {
	args := []string{"build"}

	if opts.Tag != "" {
		args = append(args, "-t", opts.Tag)
	}
	if opts.Dockerfile != "" {
		args = append(args, "-f", opts.Dockerfile)
	}
	if opts.Platform != "" {
		args = append(args, "--platform", opts.Platform)
	}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}

	contextDir := opts.ContextDir
	if contextDir == "" {
		contextDir = "."
	}
	args = append(args, contextDir)

	cmd := exec.CommandContext(ctx, "podman", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman build failed: %s: %w", stderr.String(), err)
	}

	// Podman outputs the image ID on the last line
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	imageID := ""
	if len(lines) > 0 {
		imageID = strings.TrimSpace(lines[len(lines)-1])
	}

	return &BuildResult{
		ImageID: imageID,
		Tag:     opts.Tag,
	}, nil
}

func (b *PodmanBuilder) Push(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, "podman", "push", image)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman push failed: %s: %w", stderr.String(), err)
	}
	return nil
}
