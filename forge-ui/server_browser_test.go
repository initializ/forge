package forgeui

import (
	"reflect"
	"testing"
)

// TestBrowserCommand pins the dashboard's platform→launcher selection.
// The Windows branch must use `rundll32 url.dll,FileProtocolHandler`,
// not `cmd /c start` — the latter lets cmd's parser split the URL on
// `&`, silently breaking the moment the dashboard URL gains a query
// param. Asserts the argv per GOOS and that the URL rides as a single,
// un-split trailing argument.
func TestBrowserCommand(t *testing.T) {
	const multiParam = "http://localhost:8080/?agent=x&tab=logs"
	cases := []struct {
		goos string
		args []string // full argv incl. arg0; nil = unsupported → nil cmd
	}{
		{"darwin", []string{"open", multiParam}},
		{"linux", []string{"xdg-open", multiParam}},
		{"windows", []string{"rundll32", "url.dll,FileProtocolHandler", multiParam}},
		{"plan9", nil},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			cmd := browserCommand(tc.goos, multiParam)
			if tc.args == nil {
				if cmd != nil {
					t.Fatalf("unsupported %s must yield nil, got %v", tc.goos, cmd.Args)
				}
				return
			}
			if cmd == nil {
				t.Fatalf("%s yielded nil command", tc.goos)
			}
			if !reflect.DeepEqual(cmd.Args, tc.args) {
				t.Fatalf("%s argv = %v, want %v", tc.goos, cmd.Args, tc.args)
			}
			if last := cmd.Args[len(cmd.Args)-1]; last != multiParam {
				t.Errorf("%s: URL arg mutated/split: got %q, want %q", tc.goos, last, multiParam)
			}
		})
	}
}
