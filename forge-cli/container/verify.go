package container

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Verify performs a smoke test on a built container image.
// It starts the container, waits for startup, checks /healthz and /.well-known/agent.json,
// then stops and removes the container.
func Verify(ctx context.Context, imageTag string) error {
	// Find a free port
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("finding free port: %w", err)
	}

	containerName := fmt.Sprintf("forge-verify-%d", port)

	// Start container
	runArgs := []string{
		"run", "-d",
		"--name", containerName,
		"-p", fmt.Sprintf("%d:8080", port),
		imageTag,
	}

	out, err := exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("starting container: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Ensure cleanup
	defer func() {
		exec.Command("docker", "stop", containerName).Run()     //nolint:errcheck
		exec.Command("docker", "rm", "-f", containerName).Run() //nolint:errcheck
	}()

	// Wait for startup
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}

	if err := waitForHealthy(ctx, client, baseURL+"/healthz", 30*time.Second); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	// Check /.well-known/agent.json
	resp, err := client.Get(baseURL + "/.well-known/agent.json")
	if err != nil {
		return fmt.Errorf("fetching agent.json: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agent.json returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

func waitForHealthy(ctx context.Context, client *http.Client, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}
