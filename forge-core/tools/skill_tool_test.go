package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeExecutor captures the command + args its Run was called with so
// tests can assert on the argv shape the SkillTool produced.
type fakeExecutor struct {
	lastCommand string
	lastArgs    []string
	lastStdin   []byte
}

func (f *fakeExecutor) Run(_ context.Context, command string, args []string, stdin []byte) (string, error) {
	f.lastCommand = command
	f.lastArgs = append([]string(nil), args...)
	f.lastStdin = append([]byte(nil), stdin...)
	return "ok", nil
}

// TestNewSkillTool_RunsViaBash pins the script-backed contract: argv
// is [bash <scriptPath> <jsonArgs>]. The JSON lands at $1 from the
// script's POV (after the script path slot bash consumes).
func TestNewSkillTool_RunsViaBash(t *testing.T) {
	fe := &fakeExecutor{}
	tool := NewSkillTool("demo", "demo desc", "input (string)", "skills/demo.sh", fe)

	jsonArgs := json.RawMessage(`{"input":"hello"}`)
	if _, err := tool.Execute(context.Background(), jsonArgs); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fe.lastCommand != "bash" {
		t.Errorf("script-backed skill should exec via bash; got command=%q", fe.lastCommand)
	}
	if got, want := len(fe.lastArgs), 2; got != want {
		t.Fatalf("argv len = %d, want %d (script path + json); argv=%v", got, want, fe.lastArgs)
	}
	if fe.lastArgs[0] != "skills/demo.sh" {
		t.Errorf("argv[0] = %q, want script path", fe.lastArgs[0])
	}
	if fe.lastArgs[1] != string(jsonArgs) {
		t.Errorf("argv[1] = %q, want JSON args", fe.lastArgs[1])
	}
}

// TestNewBinarySkillTool_RunsBinaryDirectly is the issue #182 pin:
// when the skill is `runtime: binary`, argv is [<binary> <jsonArgs>]
// with no bash fork in front. The binary receives the JSON at $1
// directly (same input contract as scripts so the on-the-wire shape
// is uniform).
func TestNewBinarySkillTool_RunsBinaryDirectly(t *testing.T) {
	fe := &fakeExecutor{}
	tool := NewBinarySkillTool("infil_run", "wraps infil", "input (string)", "/usr/local/bin/infil", fe)

	jsonArgs := json.RawMessage(`{"input":"hello"}`)
	if _, err := tool.Execute(context.Background(), jsonArgs); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fe.lastCommand != "/usr/local/bin/infil" {
		t.Errorf("binary-backed skill should exec the binary directly; got command=%q", fe.lastCommand)
	}
	if got, want := len(fe.lastArgs), 1; got != want {
		t.Fatalf("argv len = %d, want %d (json only); argv=%v", got, want, fe.lastArgs)
	}
	if fe.lastArgs[0] != string(jsonArgs) {
		t.Errorf("argv[0] = %q, want JSON args", fe.lastArgs[0])
	}
}
