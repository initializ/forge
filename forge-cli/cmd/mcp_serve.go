package cmd

import (
	"os"

	"github.com/initializ/forge/forge-cli/internal/surface"
	"github.com/initializ/forge/forge-cli/internal/surface/mcpserver"
	"github.com/spf13/cobra"
)

// mcpServeCmd is the durable forge MCP server Claude Code spawns over stdio. It
// is registered once with `claude mcp add ... -- forge mcp-serve` (user scope)
// by the surface, so it is available in every Claude Code session — including a
// direct resume — exposing forge_docs + the structured forge-ops. Hidden: it is
// wiring, not a user-facing command.
var mcpServeCmd = &cobra.Command{
	Use:          "mcp-serve",
	Short:        "Run the forge MCP server over stdio (used by Claude Code)",
	Hidden:       true,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		workDir, err := os.Getwd()
		if err != nil {
			return err
		}
		// Tools operate on Claude Code's working directory (its cwd when it
		// spawned us), which is the project the developer is building in.
		return mcpserver.ServeStdio(cmd.Context(), surface.MCPToolset(workDir), os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(mcpServeCmd)
}
