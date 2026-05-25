package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// mcpCmd is the root for "forge mcp" subcommands. Phase 1 ships
// list / test / login / logout.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage Model Context Protocol (MCP) servers",
	Long: `Manage Model Context Protocol (MCP) servers declared in forge.yaml.

Phase 1 supports HTTP transport only. Stdio MCP servers are on the
roadmap — see docs/mcp/index.md.

Subcommands:
  list      Show every server configured in forge.yaml + its state
  test      Connect to one server, list its tools, optionally call one
  login     Run OAuth 2.1 PKCE flow and persist tokens encrypted
  logout    Remove stored OAuth tokens for a server`,
}

// mcpListCmd shows every configured server alongside a quick reachability check.
var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured MCP servers",
	RunE:  mcpListRun,
}

// mcpTestCmd connects to one server, lists tools, optionally calls one.
var mcpTestCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Connect to a single MCP server and list its tools",
	Args:  cobra.ExactArgs(1),
	RunE:  mcpTestRun,
}

// mcpLoginCmd runs the OAuth PKCE flow for a server.
var mcpLoginCmd = &cobra.Command{
	Use:   "login <name>",
	Short: "OAuth login for an MCP server (laptop-time)",
	Args:  cobra.ExactArgs(1),
	RunE:  mcpLoginRun,
}

// mcpLogoutCmd deletes stored tokens.
var mcpLogoutCmd = &cobra.Command{
	Use:   "logout <name>",
	Short: "Delete stored OAuth tokens for an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE:  mcpLogoutRun,
}

func init() {
	mcpTestCmd.Flags().String("call", "", "tool name to invoke (optional)")
	mcpTestCmd.Flags().String("args", "{}", "JSON arguments for --call")
	mcpTestCmd.Flags().Duration("timeout", 10*time.Second, "per-RPC timeout")
	mcpCmd.AddCommand(mcpListCmd, mcpTestCmd, mcpLoginCmd, mcpLogoutCmd)
}

// loadForgeConfig reads forge.yaml from the working dir (or --config)
// AND runs the full validator. Shared by every mcp subcommand.
//
// Running ValidateForgeConfig here (review B14) means a malformed
// mcp: block surfaces with the validator's specific error
// (e.g. "auth.token_url is required for oauth") instead of cryptic
// per-server failures inside `mcp list` / `mcp test` later.
func loadForgeConfig(cmd *cobra.Command) (*types.ForgeConfig, error) {
	path := cfgFile
	if !filepath.IsAbs(path) {
		wd, _ := os.Getwd()
		path = filepath.Join(wd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg types.ForgeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	result := validate.ValidateForgeConfig(&cfg)
	if !result.IsValid() {
		return nil, fmt.Errorf("%s is invalid:\n  - %s", path,
			strings.Join(result.Errors, "\n  - "))
	}
	// Warnings printed but non-fatal.
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return &cfg, nil
}

// findServerSpec returns the MCPServer entry for the given name, or
// a helpful error listing configured names.
func findServerSpec(cfg *types.ForgeConfig, name string) (*types.MCPServer, error) {
	for i, s := range cfg.MCP.Servers {
		if s.Name == name {
			return &cfg.MCP.Servers[i], nil
		}
	}
	names := make([]string, 0, len(cfg.MCP.Servers))
	for _, s := range cfg.MCP.Servers {
		names = append(names, s.Name)
	}
	return nil, fmt.Errorf("no mcp server named %q in forge.yaml (configured: %v)", name, names)
}

// buildBearerFn returns the AuthTokenFunc to use for ad-hoc CLI calls
// (forge mcp test / list). For oauth servers, hands back a function
// that loads the stored token via OAuthFlow.BearerToken; tokens that
// were never minted yield a helpful error. For bearer/static servers,
// reads the env var. For nil auth, returns nil.
func buildBearerFn(spec types.MCPServer) mcp.AuthTokenFunc {
	if spec.Auth == nil {
		return nil
	}
	switch spec.Auth.Type {
	case "bearer", "static":
		env := spec.Auth.TokenEnv
		return func(_ context.Context) (string, error) {
			return os.Getenv(env), nil
		}
	case "oauth":
		flow := mcp.NewOAuthFlow()
		cfg := mcp.OAuthServerConfig{
			ClientID:     spec.Auth.ClientID,
			Scopes:       spec.Auth.Scopes,
			AuthorizeURL: spec.Auth.AuthorizeURL,
			TokenURL:     spec.Auth.TokenURL,
		}
		name := spec.Name
		return func(ctx context.Context) (string, error) {
			return flow.BearerToken(ctx, name, cfg)
		}
	}
	return nil
}

// connectToServer opens a Transport + Client + Initialize handshake
// against one server and returns the Client. Caller must Close it.
func connectToServer(ctx context.Context, spec types.MCPServer, timeout time.Duration) (mcp.Client, error) {
	if spec.Transport != "http" {
		return nil, fmt.Errorf("transport %q not supported in Phase 1 (stdio is on the roadmap; only http works)", spec.Transport)
	}
	httpClient := &http.Client{Timeout: timeout}
	tr, err := mcp.NewHTTPTransport(spec.URL, httpClient, buildBearerFn(spec))
	if err != nil {
		return nil, err
	}
	cli := mcp.NewClient(tr)
	go cli.Run(ctx)

	initCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := cli.Initialize(initCtx, mcp.ClientInfo{Name: "forge-cli", Version: appVersion}); err != nil {
		_ = cli.Close()
		return nil, err
	}
	if err := cli.Initialized(ctx); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return cli, nil
}
