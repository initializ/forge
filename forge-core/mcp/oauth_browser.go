package mcp

import (
	"os/exec"
	"runtime"
)

// openBrowser invokes the platform's default URL opener. This is the
// ONLY os/exec use in forge-core/mcp; it is for laptop-time
// `forge mcp login` only. Production runtime never reaches this code
// (Login is interactive — the agent process at runtime calls
// BearerToken, never Login).
func openBrowser(target string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{target}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", target}
	default:
		cmd = "xdg-open"
		args = []string{target}
	}
	return exec.Command(cmd, args...).Start() //nolint:gosec // operator-driven; target is the OAuth authorize URL we built
}
