package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/types"
	"github.com/spf13/cobra"
)

// mcpListRun prints a table of configured MCP servers, attempting a
// quick connect+initialize+tools/list against each to populate the
// State and Tools columns. Always exits 0 — failure of one server is
// reported in its row, not as a process-level error. This is
// diagnostic, not a gating health check.
func mcpListRun(cmd *cobra.Command, _ []string) error {
	cfg, err := loadForgeConfig(cmd)
	if err != nil {
		return err
	}
	if len(cfg.MCP.Servers) == 0 {
		fmt.Println("No MCP servers configured (mcp.servers[] is empty in forge.yaml)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tTRANSPORT\tURL\tSTATE\tTOOLS\tREASON"); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, s := range cfg.MCP.Servers {
		state, toolCount, reason := probeServerHealth(ctx, s)
		urlShort := s.URL
		if len(urlShort) > 60 {
			urlShort = urlShort[:57] + "..."
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			s.Name, s.Transport, urlShort, state, toolCount, reason); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// probeServerHealth opens a Client, runs Initialize + ListTools, and
// returns (state, tool-count, optional-reason). state is one of:
// ready, failed.
func probeServerHealth(parent context.Context, spec types.MCPServer) (string, int, string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cli, err := connectToServer(ctx, spec, 4*time.Second)
	if err != nil {
		return "failed", 0, shortenErr(err)
	}
	defer func() { _ = cli.Close() }()

	listCtx, listCancel := context.WithTimeout(ctx, 4*time.Second)
	defer listCancel()
	tools, err := cli.ListTools(listCtx)
	if err != nil {
		return "failed", 0, shortenErr(err)
	}
	// Apply the filter so the count matches what forge run would see.
	filtered := mcp.FilterTools(tools, spec.Tools)
	return "ready", len(filtered), ""
}

// shortenErr clips a long error to one line, ≤80 chars.
func shortenErr(err error) string {
	s := err.Error()
	if i := indexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
