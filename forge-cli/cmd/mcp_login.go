package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/types"
	"github.com/spf13/cobra"
)

// loginTokenStorePath returns the effective override (forge.yaml >
// env var > unset). Mirrors runtime's mcpTokenStorePath helper so
// laptop-time login and pod-time refresh agree on the location.
func loginTokenStorePath(cfg *types.ForgeConfig) string {
	if cfg != nil && cfg.MCP.TokenStorePath != "" {
		return cfg.MCP.TokenStorePath
	}
	return os.Getenv("MCP_TOKEN_STORE_PATH")
}

// mcpLoginRun runs the OAuth 2.1 PKCE flow against the named server, persisting
// the tokens via the encrypted llm/oauth store. Two modes:
//
//   - STANDALONE (`--url` set): resolve the connection from flags, no forge.yaml.
//     For non-forge agents (Strands, Claude, …) that have no forge.yaml but still
//     want forge to mint + store a direct MCP token.
//   - forge.yaml (no `--url`): the original path — read the server from forge.yaml.
//
// Intended for laptop-time use; pod-time tokens come from the K8s Secret mounted
// at MCP_TOKEN_STORE_PATH.
func mcpLoginRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	if url, _ := cmd.Flags().GetString("url"); url != "" {
		return mcpLoginStandalone(cmd, name)
	}
	return mcpLoginFromConfig(cmd, name)
}

// standaloneServerConfig builds an OAuthServerConfig from --url + optional flags,
// bypassing forge.yaml. Endpoints and client are discovered (RFC 9728/8414/7591)
// when omitted; --authorize-url and --token-url must be given together. Returns
// the config + the credential-store dir override (flag > MCP_TOKEN_STORE_PATH >
// unset). Separated from the flow so it is unit-testable without a browser.
func standaloneServerConfig(cmd *cobra.Command) (mcp.OAuthServerConfig, string, error) {
	url, _ := cmd.Flags().GetString("url")
	clientID, _ := cmd.Flags().GetString("client-id")
	scopes, _ := cmd.Flags().GetStringSlice("scopes")
	authorizeURL, _ := cmd.Flags().GetString("authorize-url")
	tokenURL, _ := cmd.Flags().GetString("token-url")
	storePath, _ := cmd.Flags().GetString("token-store-path")
	if storePath == "" {
		storePath = os.Getenv("MCP_TOKEN_STORE_PATH")
	}
	// RFC 8414 discovery is all-or-nothing on the pair — a lone endpoint is a
	// half-configured client the discovery path can't complete.
	if (authorizeURL == "") != (tokenURL == "") {
		return mcp.OAuthServerConfig{}, "", fmt.Errorf(
			"--authorize-url and --token-url must be set together (or both omitted for discovery)")
	}
	return mcp.OAuthServerConfig{
		ServerURL:    url,
		ClientID:     clientID,
		Scopes:       scopes,
		AuthorizeURL: authorizeURL,
		TokenURL:     tokenURL,
		// Grant "" → authorization_code (interactive 3LO), per OAuthServerConfig.
	}, storePath, nil
}

// mcpLoginStandalone logs in from flags alone (no forge.yaml).
func mcpLoginStandalone(cmd *cobra.Command, name string) error {
	sc, storePath, err := standaloneServerConfig(cmd)
	if err != nil {
		return err
	}
	return performLogin(name, sc, storePath)
}

// mcpLoginFromConfig is the original forge.yaml-driven path (unchanged behavior).
func mcpLoginFromConfig(cmd *cobra.Command, name string) error {
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
	// #324: the client_credentials (agent-principal) grant has no user and
	// no browser step — the token is minted at runtime from client_id +
	// the secret in client_secret_env. There is nothing to log in.
	if spec.Auth.Grant == "client_credentials" {
		fmt.Printf("server %q uses grant client_credentials (agent-principal) — no login needed.\n", name)
		fmt.Println("the token is minted at runtime from client_id + $" + spec.Auth.ClientSecretEnv + ".")
		return nil
	}
	return performLogin(name, mcp.OAuthServerConfig{
		ServerURL:    spec.URL, // enables RFC 9728/8414/7591 discovery when endpoints are omitted (#316)
		ClientID:     spec.Auth.ClientID,
		Scopes:       spec.Auth.Scopes,
		AuthorizeURL: spec.Auth.AuthorizeURL,
		TokenURL:     spec.Auth.TokenURL,
	}, loginTokenStorePath(cfg))
}

// performLogin runs the browser PKCE flow and stores the token. Shared by the
// standalone and forge.yaml paths so both persist identically.
func performLogin(name string, sc mcp.OAuthServerConfig, storePath string) error {
	// Apply any token-store-path override (review B11) so the laptop-side Login
	// persists into the same location the runtime will read from later.
	if storePath != "" {
		oauth.SetCredentialsDir(storePath)
	}

	flow := mcp.NewOAuthFlow()
	// Inject the CLI-side browser opener. forge-core/mcp deliberately has no
	// os/exec dependency (review B4 / spec §4.6), so the laptop-time opener
	// lives here in the CLI package.
	flow.BrowserOpener = openBrowserCLI
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Printf("opening browser to authorize Forge against %s...\n", name)
	fmt.Println("(if a browser does not open, look for the URL on stdout below)")
	if err := flow.Login(ctx, name, sc); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Println("  login: ok")
	fmt.Println("\ntokens stored at ~/.forge/credentials/mcp_" + name + ".json")
	fmt.Println("(encrypted if FORGE_PASSPHRASE is set)")
	fmt.Println("for K8s, mount this file into the pod as a Secret and point")
	fmt.Println("MCP_TOKEN_STORE_PATH at it before forge run.")
	return nil
}
