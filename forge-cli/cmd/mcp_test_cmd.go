package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/spf13/cobra"
)

// mcpTestRun runs a real initialize + tools/list against a single MCP
// server and prints the discovered tools with their truncated input
// schemas. Optional --call <tool> --args '<json>' invokes one tool
// and prints the textual result.
//
// Exits non-zero on any failure — useful in CI scripts.
func mcpTestRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	timeout, _ := cmd.Flags().GetDuration("timeout")
	callTool, _ := cmd.Flags().GetString("call")
	callArgs, _ := cmd.Flags().GetString("args")

	cfg, err := loadForgeConfig(cmd)
	if err != nil {
		return err
	}
	spec, err := findServerSpec(cfg, name)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*timeout+5*time.Second)
	defer cancel()

	fmt.Printf("connecting to %s (%s)...\n", spec.Name, spec.URL)
	cli, err := connectToServer(ctx, *spec, timeout)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = cli.Close() }()
	fmt.Println("  initialize: ok")

	listCtx, listCancel := context.WithTimeout(ctx, timeout)
	defer listCancel()
	tools, err := cli.ListTools(listCtx)
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	filtered := mcp.FilterTools(tools, spec.Tools)
	fmt.Printf("  discovered %d tool(s); %d allowed by forge.yaml filter:\n", len(tools), len(filtered))
	for _, d := range filtered {
		desc := d.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		schema := string(d.InputSchema)
		if len(schema) > 200 {
			schema = schema[:197] + "..."
		}
		fmt.Printf("    - %s__%s — %s\n      schema: %s\n", name, d.Name, desc, schema)
	}

	if callTool == "" {
		return nil
	}

	// --call invocation.
	if !toolInList(filtered, callTool) {
		return fmt.Errorf("tool %q not in filtered list (try without --call to see the list)", callTool)
	}
	fmt.Printf("\ninvoking %s...\n", callTool)
	callCtx, callCancel := context.WithTimeout(ctx, timeout)
	defer callCancel()
	res, err := cli.CallTool(callCtx, callTool, json.RawMessage(callArgs))
	if err != nil {
		return fmt.Errorf("tools/call: %w", err)
	}
	if res.IsError {
		fmt.Fprintln(os.Stderr, "  tool returned isError=true")
	}
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			fmt.Println(c.Text)
		case "image":
			fmt.Printf("[image %s, %d bytes b64]\n", c.MimeType, len(c.Data))
		default:
			fmt.Printf("[%s]\n", c.Type)
		}
	}
	if res.IsError {
		return fmt.Errorf("tool reported an error")
	}
	return nil
}

func toolInList(list []mcp.MCPToolDescriptor, name string) bool {
	for _, t := range list {
		if t.Name == name {
			return true
		}
	}
	return false
}
