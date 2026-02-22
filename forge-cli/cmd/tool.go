package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/spf13/cobra"
)

var toolCmd = &cobra.Command{
	Use:   "tool",
	Short: "Manage and inspect agent tools",
}

var toolListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available tools",
	RunE:  toolListRun,
}

var toolDescribeCmd = &cobra.Command{
	Use:   "describe <name>",
	Short: "Show tool details and schema",
	Args:  cobra.ExactArgs(1),
	RunE:  toolDescribeRun,
}

func init() {
	toolCmd.AddCommand(toolListCmd)
	toolCmd.AddCommand(toolDescribeCmd)
}

func toolListRun(cmd *cobra.Command, args []string) error {
	reg := tools.NewRegistry()
	if err := builtins.RegisterAll(reg); err != nil {
		return fmt.Errorf("registering builtins: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "NAME\tCATEGORY\tDESCRIPTION\n")

	for _, name := range reg.List() {
		t := reg.Get(name)
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name(), t.Category(), t.Description())
	}
	return w.Flush()
}

func toolDescribeRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	t := builtins.GetByName(name)
	if t == nil {
		return fmt.Errorf("unknown tool: %q", name)
	}

	fmt.Fprintf(os.Stdout, "Name:        %s\n", t.Name())
	fmt.Fprintf(os.Stdout, "Category:    %s\n", t.Category())
	fmt.Fprintf(os.Stdout, "Description: %s\n", t.Description())
	fmt.Fprintf(os.Stdout, "\nInput Schema:\n")

	var pretty json.RawMessage
	if json.Unmarshal(t.InputSchema(), &pretty) == nil {
		data, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Fprintf(os.Stdout, "%s\n", data)
	}
	return nil
}
