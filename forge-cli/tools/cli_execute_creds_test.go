package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/credentials"
	_ "github.com/initializ/forge/forge-core/credentials/static" // register static
)

// captureSink records emitted audit events so the test can assert on
// them without wiring the full AuditLogger machinery.
type captureSink struct {
	mu     sync.Mutex
	events []struct {
		name   string
		fields map[string]any
	}
}

func (c *captureSink) Emit(_ context.Context, name string, fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, struct {
		name   string
		fields map[string]any
	}{name, fields})
}

func (c *captureSink) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.name)
	}
	return out
}

// TestCLIExecute_JITCredentialsInjectedIntoEnv is the acceptance test
// for governance R9: a CredentialSpec routed to cli_execute results
// in fresh env vars appearing on the subprocess.
func TestCLIExecute_JITCredentialsInjectedIntoEnv(t *testing.T) {
	sink := &captureSink{}
	inj, err := credentials.NewInjector(
		context.Background(),
		credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "cli_execute",
			Binary:   "env",
			Provider: "static",
			Spec: json.RawMessage(`{
				"env": {"AWS_ACCESS_KEY_ID": "AKIAJITCREDS", "AWS_SECRET_ACCESS_KEY": "s3cr3t"},
				"ttl": "15m"
			}`),
		}},
		sink,
	)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"env"},
		TimeoutSeconds:  10,
	}).WithCredentialInjector(inj)

	// Skip if `env` isn't in PATH — rare, but honour the guard so CI
	// on minimal containers doesn't flake.
	avail, _ := tool.Availability()
	if !containsString(avail, "env") {
		t.Skip("`env` binary not available in PATH")
	}

	args, _ := json.Marshal(cliExecuteArgs{Binary: "env", Args: nil})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result cliExecuteResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parsing tool output: %v", err)
	}

	if !strings.Contains(result.Stdout, "AWS_ACCESS_KEY_ID=AKIAJITCREDS") {
		t.Errorf("subprocess env missing JIT access key.\nstdout: %s", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "AWS_SECRET_ACCESS_KEY=s3cr3t") {
		t.Errorf("subprocess env missing JIT secret.\nstdout: %s", result.Stdout)
	}

	// Verify audit events fired: credential_issued then credential_revoked.
	names := sink.names()
	if len(names) < 2 {
		t.Fatalf("expected 2+ audit events, got %d: %v", len(names), names)
	}
	if names[0] != "credential_issued" {
		t.Errorf("first event should be credential_issued, got %q", names[0])
	}
	if names[len(names)-1] != "credential_revoked" {
		t.Errorf("last event should be credential_revoked, got %q", names[len(names)-1])
	}
}

// TestCLIExecute_NoInjectorNoop guards backward compatibility:
// without an injector wired, cli_execute behaves exactly as pre-R9.
func TestCLIExecute_NoInjectorNoop(t *testing.T) {
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"env"},
		TimeoutSeconds:  10,
	})
	avail, _ := tool.Availability()
	if !containsString(avail, "env") {
		t.Skip("`env` binary not available in PATH")
	}
	args, _ := json.Marshal(cliExecuteArgs{Binary: "env"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result cliExecuteResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	// A dangling JIT env var must NOT appear.
	if strings.Contains(result.Stdout, "AWS_ACCESS_KEY_ID=AKIAJITCREDS") {
		t.Errorf("subprocess saw JIT env despite no injector: %s", result.Stdout)
	}
}

// TestCLIExecute_InjectorSkipsNonMatchingBinary verifies binary
// scoping — a spec pinned to `aws` must not inject on `env` calls.
func TestCLIExecute_InjectorSkipsNonMatchingBinary(t *testing.T) {
	sink := &captureSink{}
	inj, err := credentials.NewInjector(
		context.Background(),
		credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "cli_execute",
			Binary:   "aws", // pinned to aws, not env
			Provider: "static",
			Spec:     json.RawMessage(`{"env": {"SHOULD_NOT_APPEAR": "x"}}`),
		}},
		sink,
	)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"env"},
		TimeoutSeconds:  10,
	}).WithCredentialInjector(inj)
	avail, _ := tool.Availability()
	if !containsString(avail, "env") {
		t.Skip("`env` binary not available in PATH")
	}
	args, _ := json.Marshal(cliExecuteArgs{Binary: "env"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "SHOULD_NOT_APPEAR") {
		t.Errorf("binary-scoped credential leaked to non-matching binary: %s", out)
	}
	// No credential_issued should have fired since no spec matched.
	for _, name := range sink.names() {
		if name == "credential_issued" {
			t.Errorf("unexpected credential_issued for non-matching binary")
		}
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
