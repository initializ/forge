package cmd

import (
	"os/exec"
	"runtime"
)

// openBrowserCLI invokes the platform's default URL opener. This is
// the only os/exec use in the OAuth login path; it lives in the CLI
// package (not forge-core/mcp) because the runtime image must NOT
// link os/exec for MCP code — see spec §4.6 and review B4. The CLI
// `forge mcp login` command is laptop-only and never runs inside
// the agent runtime.
func openBrowserCLI(target string) error {
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
