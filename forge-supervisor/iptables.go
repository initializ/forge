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

// SetupIPTables configures iptables (nat table) to redirect outgoing TCP traffic
// from UID 1000 to the local proxy on redirectPort. Runs as UID 0 (supervisor),
// so its own traffic is NOT redirected — only the agent's UID 1000 traffic is.
// Logs a warning and continues if iptables is not available.
func SetupIPTables(ctx context.Context, uid int, proxyPort int) error {
	if !isIPTablesAvailable() {
		log.Printf("WARN: iptables not available, skipping redirect setup (cap_net_admin may be denied)")
		return nil
	}

	cleanupIPTables(ctx)

	chain := "FORGE_SUPERVISOR"
	uidStr := fmt.Sprintf("%d", uid)
	portStr := fmt.Sprintf("%d", proxyPort)

	// REDIRECT target is only valid in the nat table
	cmds := []struct {
		name string
		args []string
	}{
		// Create custom chain in nat table
		{"iptables", []string{"-t", "nat", "-N", chain}},
		// Match outgoing TCP from UID 1000, jump to custom chain
		{"iptables", []string{"-t", "nat", "-A", "OUTPUT", "-m", "owner", "--uid-owner", uidStr, "-p", "tcp", "-j", chain}},
		// Redirect to proxy port
		{"iptables", []string{"-t", "nat", "-A", chain, "-p", "tcp", "-j", "REDIRECT", "--to-port", portStr}},
	}

	for _, cmd := range cmds {
		if err := runIPTables(ctx, cmd.name, cmd.args...); err != nil {
			if strings.Contains(err.Error(), "Chain already exists") {
				continue
			}
			log.Printf("WARN: iptables setup failed: %v", err)
			return nil
		}
	}

	log.Printf("INFO: iptables nat redirect configured: UID %d -> port %d", uid, proxyPort)
	return nil
}

// isIPTablesAvailable checks if iptables exists and is executable.
func isIPTablesAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "iptables", "--version")
	return cmd.Run() == nil
}

// cleanupIPTables removes any existing FORGE_SUPERVISOR chain rules.
func cleanupIPTables(ctx context.Context) {
	chain := "FORGE_SUPERVISOR"
	uidStr := targetUID

	runIPTables(ctx, "iptables", "-t", "nat", "-F", chain)
	runIPTables(ctx, "iptables", "-t", "nat", "-D", "OUTPUT", "-m", "owner", "--uid-owner", uidStr, "-p", "tcp", "-j", chain)
	runIPTables(ctx, "iptables", "-t", "nat", "-X", chain)
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
