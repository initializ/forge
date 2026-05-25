package mcp

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestB4_PackageHasNoOsExecImport is a static-source check pinning
// the spec §4.6 / review B4 invariant: forge-core/mcp must NOT
// import os/exec. The previous PR shipped oauth_browser.go in this
// package, which silently linked os/exec into every runtime binary
// even though the symbol was unreachable from `forge run`. Now the
// browser-opener lives in forge-cli/cmd/mcp_browser.go where the
// CLI is allowed to use os/exec.
//
// A regression would mean ANY future contributor accidentally
// re-introducing os/exec into the package would fail this test in
// CI before the binary ships.
func TestB4_PackageHasNoOsExecImport(t *testing.T) {
	t.Parallel()
	// Glob for non-test sources. _test.go files MAY use os/exec
	// (none currently do, but the rule is about runtime binary
	// linkage which test files don't affect).
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		// Test ran from an unexpected working directory; force the
		// known path.
		dir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("no .go files in %s — test must run from forge-core/mcp", dir)
	}

	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		ast, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", f, err)
			continue
		}
		for _, imp := range ast.Imports {
			if imp.Path.Value == `"os/exec"` {
				t.Errorf("%s imports %s — forbidden in forge-core/mcp (spec §4.6, review B4). Move the os/exec use to forge-cli/cmd/ and inject via OAuthFlow.BrowserOpener.", f, imp.Path.Value)
			}
		}
	}
}

// TestB4_LoginFailsFastOnNilBrowserOpener pins the runtime contract:
// if a caller forgets to inject a BrowserOpener, Login returns
// ErrProtocolError immediately rather than the previous behavior
// of silently falling back to a default that shelled out via
// os/exec.
func TestB4_LoginFailsFastOnNilBrowserOpener(t *testing.T) {
	setupCredsHome(t)
	f := NewOAuthFlow()
	// f.BrowserOpener intentionally left nil.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := f.Login(ctx, "x", OAuthServerConfig{
		ClientID:     "c",
		AuthorizeURL: "https://example.com/a",
		TokenURL:     "https://example.com/t",
	})
	if err == nil {
		t.Fatal("expected error from nil BrowserOpener")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("err = %v, want wrap of ErrProtocolError", err)
	}
	for _, want := range []string{"BrowserOpener", "review B4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q hint: %v", want, err)
		}
	}
}

// TestB4_LoginUsesInjectedOpener_NotSomeDefault double-checks the
// happy path explicitly: Login MUST call the caller-provided
// BrowserOpener — there is no fallback. A future regression that
// re-introduces defaultBrowserOpener would either short-circuit
// this test (opener not called) or be caught by the static-import
// test above.
func TestB4_LoginUsesInjectedOpener_NotSomeDefault(t *testing.T) {
	setupCredsHome(t)
	var openerCalls int
	f := NewOAuthFlow()
	f.BrowserOpener = func(url string) error {
		openerCalls++
		// Don't actually drive the callback — we just want to confirm
		// the opener was called. Login will hang waiting for the
		// callback, so cancel ctx shortly.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = f.Login(ctx, "x", OAuthServerConfig{
		ClientID:     "c",
		AuthorizeURL: "https://example.com/a",
		TokenURL:     "https://example.com/t",
	})
	if openerCalls != 1 {
		t.Errorf("BrowserOpener call count = %d, want 1", openerCalls)
	}
}
