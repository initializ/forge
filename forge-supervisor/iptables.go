package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

const (
	redirectPort = 15001
	targetUID    = "1000"
	waitTimeout  = 5 * time.Second
)

// SetupIPTables configures iptables to redirect outgoing TCP traffic from UID 1000
// to the local proxy on redirectPort. It logs a warning and continues if iptables
// is not available (e.g., cap_net_admin denied).
func SetupIPTables(ctx context.Context, uid int, proxyPort int) error {
	// Check if iptables is available
	if !isIPTablesAvailable() {
		log.Printf("WARN: iptables not available, skipping redirect setup (cap_net_admin may be denied)")
		return nil
	}

	// Clean up any existing rules first
	cleanupIPTables(ctx)

	chain := "FORGE_SUPERVISOR"

	cmds := []struct {
		name string
		args []string
	}{
		// Create custom chain
		{"iptables", []string{"-N", chain}},
		// Match owner UID
		{"iptables", []string{"-A", "OUTPUT", "-m", "owner", "--uid-owner", fmt.Sprintf("%d", uid), "-p", "tcp", "-j", chain}},
		// Redirect to proxy port in the custom chain
		{"iptables", []string{"-A", chain, "-p", "tcp", "-j", "REDIRECT", "--to-port", fmt.Sprintf("%d", proxyPort)}},
	}

	for _, cmd := range cmds {
		if err := runIPTables(ctx, cmd.name, cmd.args...); err != nil {
			// If chain already exists, that's OK
			if strings.Contains(err.Error(), "Chain already exists") {
				continue
			}
			log.Printf("WARN: iptables setup failed: %v", err)
			return nil // Don't fail, just warn
		}
	}

	log.Printf("INFO: iptables redirect configured for UID %d -> port %d", uid, proxyPort)
	return nil
}

// isIPTablesAvailable checks if iptables command exists and is executable.
func isIPTablesAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "iptables", "--version")
	return cmd.Run() == nil
}

// cleanupIPTables removes any existing FORGE_SUPERVISOR chain rules.
func cleanupIPTables(ctx context.Context) {
	chain := "FORGE_SUPERVISOR"

	// Try to flush the chain
	runIPTables(ctx, "iptables", "-F", chain)

	// Try to delete the chain reference from OUTPUT
	runIPTables(ctx, "iptables", "-D", "OUTPUT", "-m", "owner", "--uid-owner", targetUID, "-p", "tcp", "-j", chain)

	// Try to delete the chain itself
	runIPTables(ctx, "iptables", "-X", chain)
}

// runIPTables executes an iptables command with the given arguments.
func runIPTables(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %v: %s: %w", args, string(out), err)
	}
	return nil
}
