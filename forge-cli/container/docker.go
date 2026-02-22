package container

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DockerBuilder builds container images using the docker CLI.
type DockerBuilder struct{}

func (b *DockerBuilder) Name() string { return "docker" }

func (b *DockerBuilder) Available() bool {
	return exec.Command("docker", "info").Run() == nil
}

func (b *DockerBuilder) Build(ctx context.Context, opts BuildOptions) (*BuildResult, error) {
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

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker build failed: %s: %w", stderr.String(), err)
	}

	imageID := parseImageID(string(out))
	return &BuildResult{
		ImageID: imageID,
		Tag:     opts.Tag,
	}, nil
}

func (b *DockerBuilder) Push(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, "docker", "push", image)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker push failed: %s: %w", stderr.String(), err)
	}
	return nil
}

// parseImageID extracts the image ID from docker build output.
func parseImageID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// Docker outputs "Successfully built <id>" or just a sha256 hash
		if strings.HasPrefix(line, "Successfully built ") {
			return strings.TrimPrefix(line, "Successfully built ")
		}
		if strings.HasPrefix(line, "sha256:") {
			return line
		}
	}
	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return ""
}
