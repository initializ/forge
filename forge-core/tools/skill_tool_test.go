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

// A property key outside the provider constraint (Anthropic
// ^[a-zA-Z0-9_.-]{1,64}$) bricks every LLM call of the agent — the runner
// skips such tools at registration, keyed on this helper (field-hit
// 2026-07-22: a param named "pod name").
func TestInvalidSchemaPropertyKeys(t *testing.T) {
	schema := InputSpecToSchema("pod name (string, required), namespace (string), ok_key.v2 (string)")
	bad := InvalidSchemaPropertyKeys(schema)
	if len(bad) != 1 || bad[0] != "pod name" {
		t.Fatalf("want [pod name], got %v", bad)
	}

	if bad := InvalidSchemaPropertyKeys(InputSpecToSchema("a (string), b_c (integer)")); bad != nil {
		t.Fatalf("clean schema must return nil, got %v", bad)
	}
	if bad := InvalidSchemaPropertyKeys(nil); bad != nil {
		t.Fatalf("nil schema must return nil, got %v", bad)
	}
	if bad := InvalidSchemaPropertyKeys([]byte("not json")); bad != nil {
		t.Fatalf("unparseable schema must return nil, got %v", bad)
	}
	long := InvalidSchemaPropertyKeys([]byte(`{"properties":{"` + string(make65()) + `":{"type":"string"}}}`))
	if len(long) != 1 {
		t.Fatalf("65-char key must violate, got %v", long)
	}
	// Deterministic order for multi-key messages.
	multi := InvalidSchemaPropertyKeys([]byte(`{"properties":{"z bad":{},"a bad":{}}}`))
	if len(multi) != 2 || multi[0] != "a bad" || multi[1] != "z bad" {
		t.Fatalf("want sorted [a bad, z bad], got %v", multi)
	}
}

func make65() []byte {
	b := make([]byte, 65)
	for i := range b {
		b[i] = 'a'
	}
	return b
}

// The platform materializer writes "**Input:** `pod_name` (string, required),
// `ns` (string)" — backticked names, comma inside the paren annotation. The
// pre-fix comma-split manufactured a bogus "required)" property and kept the
// backticks in the key; both violate the provider pattern and bricked every
// call of any platform-built script tool with a required param.
func TestInputSpecToSchemaPlatformFormat(t *testing.T) {
	schema := InputSpecToSchema("`pod_name` (string, required), `ns` (string)")
	var doc struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Properties) != 2 {
		t.Fatalf("want 2 properties, got %v", doc.Properties)
	}
	if _, ok := doc.Properties["pod_name"]; !ok {
		t.Fatalf("backticks not stripped: %v", doc.Properties)
	}
	if _, ok := doc.Properties["ns"]; !ok {
		t.Fatalf("ns missing: %v", doc.Properties)
	}
	if _, bogus := doc.Properties["required)"]; bogus {
		t.Fatal("comma inside parens manufactured a bogus property")
	}
	if len(doc.Required) != 1 || doc.Required[0] != "pod_name" {
		t.Fatalf("required flag lost: %v", doc.Required)
	}
	if bad := InvalidSchemaPropertyKeys(schema); bad != nil {
		t.Fatalf("platform-format schema must be provider-valid, got violations %v", bad)
	}
}
