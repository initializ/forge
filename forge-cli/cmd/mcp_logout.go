package cmd

import (
	"fmt"
	"os"

	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/mcp"
	"github.com/spf13/cobra"
)

// mcpLogoutRun deletes the stored OAuth token for an MCP server.
// Idempotent — runs cleanly even if no token was stored or the
// server entry has been removed from forge.yaml.
//
// We deliberately do NOT load forge.yaml here (review B13): the
// store key is deterministic from the name, and operators who
// already removed the server entry should still be able to clean
// up the leftover token. The previous lookup blocked exactly that
// workflow.
func mcpLogoutRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	// Honor MCP_TOKEN_STORE_PATH env override (review B11) so
	// logout deletes from the same location login wrote to. We do
	// NOT load forge.yaml — see the docstring.
	if path := os.Getenv("MCP_TOKEN_STORE_PATH"); path != "" {
		oauth.SetCredentialsDir(path)
	}
	flow := mcp.NewOAuthFlow()
	if err := flow.Logout(name); err != nil {
		return fmt.Errorf("logout %s: %w", name, err)
	}
	fmt.Printf("removed stored OAuth tokens for %q\n", name)
	return nil
}
