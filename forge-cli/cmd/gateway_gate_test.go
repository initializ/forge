package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestGatewayGateApplies pins which commands the login gate covers: the
// LLM-touching ones (run, try, serve/serve start) and NOT management verbs
// (serve stop/status/logs, tool list, auth *, offline commands).
func TestGatewayGateApplies(t *testing.T) {
	root := &cobra.Command{Use: "forge"}

	run := &cobra.Command{Use: "run"}
	try := &cobra.Command{Use: "try"}
	validate := &cobra.Command{Use: "validate"}
	settings := &cobra.Command{Use: "settings"}

	serve := &cobra.Command{Use: "serve"}
	serveStart := &cobra.Command{Use: "start"}
	serveStop := &cobra.Command{Use: "stop"}
	serve.AddCommand(serveStart, serveStop)

	tool := &cobra.Command{Use: "tool"}
	toolList := &cobra.Command{Use: "list"}
	tool.AddCommand(toolList)

	auth := &cobra.Command{Use: "auth"}
	authLogin := &cobra.Command{Use: "login"}
	auth.AddCommand(authLogin)

	root.AddCommand(run, try, validate, settings, serve, tool, auth)

	cases := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"run", run, true},
		{"try", try, true},
		{"serve (bare)", serve, true},
		{"serve start", serveStart, true},
		{"serve stop", serveStop, false},
		{"tool list", toolList, false},
		{"auth login", authLogin, false},
		{"validate", validate, false},
		{"settings", settings, false},
		{"root", root, false},
	}
	for _, c := range cases {
		if got := gatewayGateApplies(c.cmd); got != c.want {
			t.Errorf("gatewayGateApplies(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
