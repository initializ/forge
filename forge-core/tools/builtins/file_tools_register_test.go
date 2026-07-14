package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

// TestRegisterFileTools_RegistersTheFour pins that the #268 wiring exposes the
// general file read/write/edit/patch builtins (previously dead code, reachable
// only from tests).
func TestRegisterFileTools_RegistersTheFour(t *testing.T) {
	reg := tools.NewRegistry()
	if err := RegisterFileTools(reg, t.TempDir()); err != nil {
		t.Fatalf("RegisterFileTools: %v", err)
	}
	for _, name := range []string{"file_read", "file_write", "file_edit", "file_patch"} {
		if reg.Get(name) == nil {
			t.Errorf("expected %q to be registered", name)
		}
	}
}

// TestFileTools_ConfinePathTraversal is the #235-class guard the issue asks
// for: every general file tool must reject a `../../etc/passwd`-style escape
// out of the confinement root. Regression guard against re-opening the
// cli_execute-class escape through the newly-wired file surface.
func TestFileTools_ConfinePathTraversal(t *testing.T) {
	root := t.TempDir()
	ft := FileTools(root)

	// Each tool's minimal args plus a traversal path. All must error before
	// touching /etc/passwd.
	cases := map[string]map[string]any{
		"file_read":  {"path": "../../../../../../etc/passwd"},
		"file_write": {"path": "../../../../../../etc/passwd", "content": "x"},
		"file_edit":  {"path": "../../../../../../etc/passwd", "old_text": "a", "new_text": "b"},
		"file_patch": {"path": "../../../../../../etc/passwd", "operations": []map[string]any{
			{"action": "write", "path": "../../../../../../etc/passwd", "content": "x"},
		}},
	}

	for _, tool := range ft {
		args, ok := cases[tool.Name()]
		if !ok {
			t.Fatalf("no traversal case for tool %q", tool.Name())
		}
		raw, _ := json.Marshal(args)
		_, err := tool.Execute(context.Background(), raw)
		if err == nil {
			t.Errorf("%s: traversal path was NOT rejected (path confinement regressed)", tool.Name())
			continue
		}
		if !strings.Contains(err.Error(), "outside the working directory") {
			t.Errorf("%s: expected a confinement error, got %v", tool.Name(), err)
		}
	}
}
