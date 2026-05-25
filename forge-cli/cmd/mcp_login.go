package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/spf13/cobra"
)

// mcpLoginRun runs the OAuth 2.1 PKCE flow against the named server,
// persisting the resulting tokens via the encrypted llm/oauth store.
// Intended for laptop-time use; pod-time tokens come from the K8s
// Secret mounted at MCP_TOKEN_STORE_PATH.
func mcpLoginRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := loadForgeConfig(cmd)
	if err != nil {
		return err
	}
	spec, err := findServerSpec(cfg, name)
	if err != nil {
		return err
	}
	if spec.Auth == nil || spec.Auth.Type != "oauth" {
		return fmt.Errorf("server %q does not declare oauth (auth.type=%q)", name,
			func() string {
				if spec.Auth == nil {
					return ""
				}
				return spec.Auth.Type
			}())
	}

	flow := mcp.NewOAuthFlow()
	// Inject the CLI-side browser opener. forge-core/mcp deliberately
	// has no os/exec dependency (review B4 / spec §4.6), so the
	// laptop-time opener lives here in the CLI package.
	flow.BrowserOpener = openBrowserCLI
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Printf("opening browser to authorize Forge against %s...\n", name)
	fmt.Println("(if a browser does not open, look for the URL on stdout below)")
	if err := flow.Login(ctx, name, mcp.OAuthServerConfig{
		ClientID:     spec.Auth.ClientID,
		Scopes:       spec.Auth.Scopes,
		AuthorizeURL: spec.Auth.AuthorizeURL,
		TokenURL:     spec.Auth.TokenURL,
	}); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Println("  login: ok")
	fmt.Println("\ntokens stored at ~/.forge/credentials/mcp_" + name + ".json")
	fmt.Println("(encrypted if FORGE_PASSPHRASE is set)")
	fmt.Println("for K8s, mount this file into the pod as a Secret and point")
	fmt.Println("MCP_TOKEN_STORE_PATH at it before forge run.")
	return nil
}
