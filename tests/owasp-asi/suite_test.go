// Package owaspasi is the OWASP Top 10 for Agentic Applications (ASI 2026)
// conformance suite for Forge. It is eval-first: every entry is
// hypothesis -> dataset -> grader -> measured rate with a threshold, not a
// single hand-picked assertion.
//
// Two tiers (mirroring the agent-redteam methodology):
//
//   - Instrumented tier (authoritative for "contained"): drives real Forge
//     controls in-process and asserts that the control FIRED via an
//     instrumented signal (an audit event, a policy-violation error, a
//     blocked-egress record). A containment claim MUST have such a signal.
//   - Black-box tier: observed behavior only; may flag suspected leakage but
//     may not upgrade a grade to Enforced on its own. The cmd/a2a-redteam
//     tooling the plan references is NOT present in this repo, so the
//     black-box tier is documented but not wired here (see README).
//
// xfail cases (t.Skip with a reason + backlog issue) enumerate the unmet
// surface without breaking the green suite for shipped scope.
package owaspasi

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-cli/tools"
)

// loadJSONL reads a JSONL dataset into a slice of generic maps. Blank lines and
// # comments are skipped. ASCII-only by construction.
func loadJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open dataset %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad JSONL line in %s: %v", path, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

// loadLines reads a newline-delimited dataset, skipping blanks and # comments.
func loadLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dataset %s: %v", path, err)
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// newCLIExec builds a real CLIExecuteTool confined to workDir with the given
// allowlist. This is the actual runtime sandbox, not a stub.
func newCLIExec(allowed []string, workDir string) *tools.CLIExecuteTool {
	return tools.NewCLIExecuteTool(tools.CLIExecuteConfig{
		AllowedBinaries: allowed,
		WorkDir:         workDir,
	})
}

// argsField extracts a []string from a decoded JSONL "args" field.
func argsField(m map[string]any) []string {
	raw, _ := m["args"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// str returns the string value of a decoded field, or "".
func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
