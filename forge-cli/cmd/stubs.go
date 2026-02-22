package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func stubRun(name string) func(cmd *cobra.Command, args []string) {
	return func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s: not yet implemented\n", name)
	}
}
