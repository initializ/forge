package cmd

import (
	"fmt"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/spf13/cobra"
)

// mcpLogoutRun deletes the stored OAuth token for an MCP server.
// Idempotent — runs cleanly even if no token was stored.
func mcpLogoutRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	// Loading forge.yaml is not strictly required for logout, but
	// validating the name exists gives the operator a clearer error
	// than a silent no-op.
	cfg, err := loadForgeConfig(cmd)
	if err != nil {
		return err
	}
	if _, err := findServerSpec(cfg, name); err != nil {
		return err
	}

	flow := mcp.NewOAuthFlow()
	if err := flow.Logout(name); err != nil {
		return fmt.Errorf("logout %s: %w", name, err)
	}
	fmt.Printf("removed stored OAuth tokens for %q\n", name)
	return nil
}
