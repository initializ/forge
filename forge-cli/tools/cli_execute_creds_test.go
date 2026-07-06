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

// TestCLIExecute_JITOverridesEnvPassthrough is the security-critical
// regression test reviewer initializ-mk asked for on #236: when an
// operator lists a JIT key (e.g. AWS_ACCESS_KEY_ID) in env_passthrough
// AND has a JIT provider stamping the same name, the JIT value MUST
// win. Otherwise the subprocess silently keeps the broader source
// creds — a privilege escalation.
//
// The pre-fix implementation relied on os/exec's env dedup keeping
// the last entry (which does work on current Go), but Forge must not
// depend on that runtime behavior for security. The fix explicitly
// strips conflicting keys from cmd.Env before appending the JIT env;
// this test asserts the JIT value wins even when we can't rely on
// exec dedup.
func TestCLIExecute_JITOverridesEnvPassthrough(t *testing.T) {
	// Seed the process env with a broad "source" value that
	// env_passthrough will propagate.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIASOURCEBROAD")

	sink := &captureSink{}
	inj, err := credentials.NewInjector(
		context.Background(),
		credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "cli_execute",
			Binary:   "env",
			Provider: "static",
			Spec: json.RawMessage(`{
				"env": {"AWS_ACCESS_KEY_ID": "AKIAJITSCOPED"}
			}`),
		}},
		sink,
	)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"env"},
		// EnvPassthrough conflicts intentionally with the JIT key.
		EnvPassthrough: []string{"AWS_ACCESS_KEY_ID"},
		TimeoutSeconds: 10,
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
	var result cliExecuteResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parsing tool output: %v", err)
	}

	// The JIT value MUST be present.
	if !strings.Contains(result.Stdout, "AWS_ACCESS_KEY_ID=AKIAJITSCOPED") {
		t.Errorf("subprocess env missing JIT-scoped key.\nstdout: %s", result.Stdout)
	}
	// The source value MUST NOT be present — belt-and-suspenders
	// stripping means only one AWS_ACCESS_KEY_ID line exists.
	if strings.Contains(result.Stdout, "AWS_ACCESS_KEY_ID=AKIASOURCEBROAD") {
		t.Errorf("source-broad key leaked past the JIT override.\nstdout: %s", result.Stdout)
	}
	// Also assert there's exactly ONE AWS_ACCESS_KEY_ID= line —
	// duplicates would mean stripping didn't fire and we were
	// relying on exec dedup. The test proves the strip pass ran.
	if got := strings.Count(result.Stdout, "AWS_ACCESS_KEY_ID="); got != 1 {
		t.Errorf("expected 1 AWS_ACCESS_KEY_ID line after strip, got %d.\nstdout: %s",
			got, result.Stdout)
	}
}

// TestStripEnvKeys unit-tests the pure helper independent of the
// subprocess plumbing, so regressions land here first.
func TestStripEnvKeys(t *testing.T) {
	env := []string{"PATH=/usr/bin", "AWS_ACCESS_KEY_ID=source", "HOME=/tmp", "AWS_SECRET_ACCESS_KEY=also-source"}
	override := map[string]string{"AWS_ACCESS_KEY_ID": "jit", "AWS_SECRET_ACCESS_KEY": "jit-sec"}
	got := stripEnvKeys(env, override)
	// PATH and HOME survive; both AWS_ keys are stripped.
	if len(got) != 2 {
		t.Fatalf("expected 2 survivors, got %d: %v", len(got), got)
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "AWS_ACCESS_KEY_ID=") || strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY=") {
			t.Errorf("conflicting key survived: %s", kv)
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
