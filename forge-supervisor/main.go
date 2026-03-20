package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/initializ/forge/forge-core/security"
)

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load egress policy
	policy, err := LoadPolicy("egress_allowlist.json")
	if err != nil {
		log.Fatalf("FATAL: failed to load policy: %v", err)
	}

	// Create domain matcher
	matcher := security.NewDomainMatcher(policy.Mode, policy.AllowedDomains)

	// Set up iptables REDIRECT for UID 1000
	if err := SetupIPTables(ctx, 1000, 15001); err != nil {
		log.Printf("WARNING: iptables setup failed (may lack CAP_NET_ADMIN): %v", err)
	}

	// Start audit logger
	audit := NewAuditLogger()

	// Start health endpoints
	denialTracker := &DenialTracker{denials: []DenialEvent{}}
	StartHealthEndpoints(denialTracker, 15000)

	// Create transparent proxy
	proxy := NewTransparentProxy(matcher, denialTracker, audit)
	if err := proxy.Start(ctx, ":15001"); err != nil {
		log.Fatalf("FATAL: failed to start proxy: %v", err)
	}

	// Privilege drop before exec
	if err := DropPrivileges(1000, 1000); err != nil {
		log.Fatalf("FATAL: failed to drop privileges: %v", err)
	}

	// Fork/exec the agent process
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
