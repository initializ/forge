//go:build manual

// Manual, network-touching demo of the browser tool stack. Excluded from the
// normal build/CI by the `manual` tag. It drives the real Manager (real
// Chromium, real EgressProxy) against a live site and prints exactly what the
// LLM would see — the indexed digest and an extract — so the token-optimized
// output can be eyeballed without an LLM or API key.
//
// Run:
//
//	cd forge-cli
//	go test -tags manual -run TestManualBrowseDemo -v ./tools/browser/
//
// Point it elsewhere (the allowlist is derived from the URL's host):
//
//	DEMO_URL=https://news.ycombinator.com go test -tags manual -run TestManualBrowseDemo -v ./tools/browser/
//
// Watch it happen in a visible window:
//
//	FORGE_BROWSER_HEADLESS=false DEMO_URL=... go test -tags manual -run TestManualBrowseDemo -v ./tools/browser/
package browser

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/initializ/forge/forge-core/security"
)

func TestManualBrowseDemo(t *testing.T) {
	bin, err := ResolveBinary()
	if err != nil {
		t.Fatalf("no browser found: %v (set FORGE_BROWSER_BIN)", err)
	}
	t.Logf("browser binary: %s", bin)

	target := os.Getenv("DEMO_URL")
	if target == "" {
		target = "https://example.com"
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("bad DEMO_URL %q: %v", target, err)
	}

	// Allowlist exactly the target host (plus its www/apex sibling), so this
	// demonstrates real egress enforcement, not dev-open.
	matcher := security.NewDomainMatcher(security.ModeAllowlist, []string{u.Hostname()})
	proxy := security.NewEgressProxy(matcher, false, nil)
	proxy.OnAttempt = func(a security.EgressAttempt) {
		verdict := "ALLOW"
		if !a.Allowed {
			verdict = "BLOCK"
		}
		t.Logf("[egress] %-5s %s", verdict, a.Domain)
	}
	proxyURL, err := proxy.Start(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck
	t.Logf("egress proxy: %s (allowlist: %s)", proxyURL, u.Hostname())

	m, err := NewManager(Config{
		BinaryPath: bin,
		Headless:   HeadlessFromEnv(),
		ProxyURL:   proxyURL,
		WorkDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Stop()

	// 1. Navigate → the digest is exactly what the LLM receives.
	snap, err := m.Navigate(u.String(), 500, 0)
	if err != nil {
		t.Fatalf("navigate %s: %v", u, err)
	}
	fmt.Println("\n================ DIGEST (what the LLM sees) ================")
	fmt.Println(buildDigest(snap))
	fmt.Printf("\n[stats] digest is %d chars for %d interactive elements\n", len(buildDigest(snap)), snap.TotalEls)

	// 2. Extract readable text, paginated.
	content, pageURL, err := m.Extract("text", "")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	preview := content
	if len(preview) > 600 {
		preview = preview[:600] + "…"
	}
	fmt.Println("\n================ EXTRACT text (first 600 chars) ============")
	fmt.Printf("url: %s\ntotal: %d chars\n\n%s\n", pageURL, len(content), preview)

	// 3. Prove egress enforcement blocks an off-allowlist host.
	if _, err := m.Navigate("https://blocked.example.test/", 0, 0); err == nil {
		t.Error("expected off-allowlist navigation to be blocked")
	} else {
		fmt.Printf("\n[egress] off-allowlist navigation correctly refused: %v\n", browseError(err))
	}
}
