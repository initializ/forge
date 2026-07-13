package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

// With compression enabled the executor stamps the tool context with
// tools.WithRelaxedLimits; tool-internal caps scale 16x so the full output
// reaches the compression layer instead of being destroyed inside the tool.

func TestTruncateOutputCtx_RelaxedByteLimit(t *testing.T) {
	t.Parallel()
	// Over the standard 50KB cap, under the relaxed 800KB cap.
	in := strings.Repeat("x", 100*1024)

	std := TruncateOutputCtx(context.Background(), in)
	if !strings.Contains(std, "output truncated") {
		t.Fatal("standard limits should truncate 100KB")
	}

	relaxed := TruncateOutputCtx(tools.WithRelaxedLimits(context.Background()), in)
	if relaxed != in {
		t.Fatalf("relaxed limits should pass 100KB through, got %d chars", len(relaxed))
	}
}

func TestTruncateOutputCtx_RelaxedLineLimit(t *testing.T) {
	t.Parallel()
	// Over the standard 2000-line cap, under the relaxed 32000-line cap.
	in := strings.Repeat("line\n", 5000)

	std := TruncateOutputCtx(context.Background(), in)
	if !strings.Contains(std, "output truncated") {
		t.Fatal("standard limits should truncate 5000 lines")
	}

	relaxed := TruncateOutputCtx(tools.WithRelaxedLimits(context.Background()), in)
	if strings.Contains(relaxed, "output truncated") {
		t.Fatal("relaxed limits should pass 5000 lines through")
	}
}

func TestTruncateOutputCtx_RelaxedStillBounded(t *testing.T) {
	t.Parallel()
	// Over even the relaxed 800KB cap — "relaxed" never means "unbounded".
	in := strings.Repeat("x", RelaxedMaxOutputBytes+1024)
	relaxed := TruncateOutputCtx(tools.WithRelaxedLimits(context.Background()), in)
	if !strings.Contains(relaxed, "output truncated") {
		t.Fatal("relaxed limits must still bound pathological output")
	}
}

// The live finding that motivated relaxed limits: grep_search's 50-line
// default silently cut the one CrashLoopBackOff row at match #78. With
// compression on, the default scales to 500.
func TestGrepSearch_RelaxedDefaultMaxResults(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "needle row %03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &grepSearchTool{pathValidator: NewPathValidator(dir)}
	args := json.RawMessage(`{"pattern":"needle"}`)

	std, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	// The Go fallback path stops silently at maxResults; the ripgrep path
	// appends a "more results" marker — either way, at most 50 matches.
	if got := strings.Count(std, "needle"); got > 50 {
		t.Fatalf("standard default should cap at 50 matches, got %d", got)
	}

	relaxed, err := g.Execute(tools.WithRelaxedLimits(context.Background()), args)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(relaxed, "needle"); got != 120 {
		t.Fatalf("relaxed default should return all 120 matches, got %d", got)
	}
	// Match #78 — the row the standard default silently dropped.
	if !strings.Contains(relaxed, "needle row 077") {
		t.Fatal("relaxed output missing the tail rows")
	}

	// An explicit max_results is the caller's choice — never overridden.
	capped, err := g.Execute(tools.WithRelaxedLimits(context.Background()), json.RawMessage(`{"pattern":"needle","max_results":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(capped, "needle"); got > 10 {
		t.Fatalf("explicit max_results ignored under relaxed limits: %d matches", got)
	}
}

// http_request previously cut response bodies at 1MB via a bare
// LimitReader — silently, mid-JSON. The cap now reports "truncated": true
// and relaxes to 4MB when compression is on.
func TestHTTPRequest_BodyCapHonestAndRelaxed(t *testing.T) {
	payload := strings.Repeat("x", httpBodyLimitBytes+512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	h := &httpRequestTool{}
	args, _ := json.Marshal(map[string]string{"method": "GET", "url": srv.URL})

	var out struct {
		Body      string `json:"body"`
		Truncated bool   `json:"truncated"`
	}

	// Standard: cut at 1MB, honestly flagged.
	raw, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatal("over-limit body must set truncated=true")
	}
	if len(out.Body) != httpBodyLimitBytes {
		t.Fatalf("body cut at %d, want %d", len(out.Body), httpBodyLimitBytes)
	}

	// Relaxed: same body passes whole, no flag.
	raw, err = h.Execute(tools.WithRelaxedLimits(context.Background()), args)
	if err != nil {
		t.Fatal(err)
	}
	out = struct {
		Body      string `json:"body"`
		Truncated bool   `json:"truncated"`
	}{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Truncated {
		t.Fatal("relaxed limits should pass a 1MB+512B body whole")
	}
	if len(out.Body) != len(payload) {
		t.Fatalf("relaxed body %d bytes, want %d", len(out.Body), len(payload))
	}
}

func TestFileRead_RelaxedDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 2500; i++ {
		fmt.Fprintf(&b, "row %04d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &fileReadTool{pathValidator: NewPathValidator(dir)}
	args := json.RawMessage(`{"path":"big.txt"}`)

	std, err := f.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	// The "(N more lines not shown)" trailer itself tips the output over
	// the 2000-line TruncateOutput cap, so the signal may be either form.
	if !strings.Contains(std, "more lines not shown") && !strings.Contains(std, "output truncated") {
		t.Fatal("standard default should cap at 2000 lines")
	}
	if strings.Contains(std, "row 2499") {
		t.Fatal("standard default should not return the tail")
	}

	relaxed, err := f.Execute(tools.WithRelaxedLimits(context.Background()), args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(relaxed, "more lines not shown") || strings.Contains(relaxed, "output truncated") {
		t.Fatal("relaxed default should return all 2500 lines")
	}
	if !strings.Contains(relaxed, "row 2499") {
		t.Fatal("relaxed output missing the tail")
	}
}
