package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/initializ/forge/forge-core/security"
)

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load egress policy — path from env or default
	policyPath := os.Getenv("FORGE_SUPERVISOR_POLICY_PATH")
	if policyPath == "" {
		policyPath = "/etc/forge/egress_allowlist.json"
	}

	policy, err := LoadPolicy(policyPath)
	if err != nil {
		log.Fatalf("FATAL: failed to load policy from %q: %v", policyPath, err)
	}

	// Create domain matcher
	matcher := security.NewDomainMatcher(policy.Mode, policy.AllowedDomains)

	// Ports from env or defaults
	proxyPort := 15001
	if p := os.Getenv("FORGE_SUPERVISOR_PROXY_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 && v < 65536 {
			proxyPort = v
		}
	}
	healthPort := 15000
	if h := os.Getenv("FORGE_SUPERVISOR_HEALTH_PORT"); h != "" {
		if v, err := strconv.Atoi(h); err == nil && v > 0 && v < 65536 {
			healthPort = v
		}
	}

	// Set up iptables REDIRECT for UID 1000 — supervisor stays UID 0
	if err := SetupIPTables(ctx, 1000, proxyPort); err != nil {
		log.Printf("WARNING: iptables setup failed (may lack CAP_NET_ADMIN): %v", err)
	}

	// Start audit logger
	audit := NewAuditLogger()

	// Start health endpoints
	denialTracker := &DenialTracker{denials: []DenialEvent{}}
	StartHealthEndpoints(denialTracker, healthPort)

	// Create transparent proxy
	proxy := NewTransparentProxy(matcher, denialTracker, audit)
	if err := proxy.Start(ctx, ":"+strconv.Itoa(proxyPort)); err != nil {
		log.Fatalf("FATAL: failed to start proxy: %v", err)
	}

	// NOTE: Do NOT drop privileges on the supervisor process.
	// The supervisor runs as UID 0 so its own traffic is not redirected.
	// Only the agent child process (exec.go) runs as UID 1000.

	// Fork/exec the agent process — runs as UID 1000 via exec.go
	agentCmd := os.Args[1:]
	if len(agentCmd) == 0 {
		agentCmd = []string{"/bin/sh", "-l"}
	}

	proc, err := ExecAgent(agentCmd)
	if err != nil {
		log.Fatalf("FATAL: failed to exec agent: %v", err)
	}

	// Forward signals to agent
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGCHLD)

	for {
		select {
		case s := <-sigChan:
			switch s {
			case syscall.SIGCHLD:
				var status syscall.WaitStatus
				syscall.Wait4(proc.Pid, &status, 0, nil)
				if status.Exited() {
					audit.LogExitEvent(proc.Pid, status.ExitStatus())
					cancel()
					return
				}
			default:
				ForwardSignal(proc.Pid, s.(syscall.Signal))
			}
		case <-ctx.Done():
			return
		}
	}
}
