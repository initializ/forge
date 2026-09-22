package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func newTestCmdForTest() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().String("url", "", "")
	c.Flags().String("token-store-path", "", "")
	return c
}

func TestResolveTestServerSpec_StandaloneFromURL(t *testing.T) {
	c := newTestCmdForTest()
	_ = c.Flags().Set("url", "https://mcp.example/x")
	_ = c.Flags().Set("token-store-path", "/tmp/creds")

	spec, store, err := resolveTestServerSpec(c, "atlassian-read")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "atlassian-read" || spec.URL != "https://mcp.example/x" {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Transport != "http" {
		t.Errorf("Transport = %q, want http (connectToServer requires it)", spec.Transport)
	}
	if spec.Auth == nil || spec.Auth.Type != "oauth" {
		t.Errorf("Auth = %+v, want oauth (so the stored token is read by name)", spec.Auth)
	}
	// Standalone has no forge.yaml allow-list → must allow-all ("*"), else
	// filterTools' empty-allow=deny-all default hides every discovered tool.
	if len(spec.Tools.Allow) != 1 || spec.Tools.Allow[0] != "*" {
		t.Errorf("Tools.Allow = %v, want [\"*\"] (show all in the diagnostic)", spec.Tools.Allow)
	}
	if store != "/tmp/creds" {
		t.Errorf("storeOverride = %q", store)
	}
}

func TestResolveTestServerSpec_StandaloneEnvStoreFallback(t *testing.T) {
	t.Setenv("MCP_TOKEN_STORE_PATH", "/env/creds")
	c := newTestCmdForTest()
	_ = c.Flags().Set("url", "https://mcp.example/x")

	_, store, err := resolveTestServerSpec(c, "atlassian-read")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store != "/env/creds" {
		t.Errorf("storeOverride = %q, want env fallback", store)
	}
}

func TestResolveTestServerSpec_StandaloneRejectsBadName(t *testing.T) {
	c := newTestCmdForTest()
	_ = c.Flags().Set("url", "https://mcp.example/x")

	if _, _, err := resolveTestServerSpec(c, "x/../../evil"); err == nil {
		t.Fatal("expected a validation error for a traversal-y name")
	}
}

func TestValidateTestURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://mcp.example/x", true},
		{"http://localhost:9000/mcp", true}, // loopback http allowed (dev IdP)
		{"http://127.0.0.1:9000/mcp", true}, // loopback
		{"http://[::1]:9000/mcp", true},     // ipv6 loopback
		{"http://mcp.example/x", false},     // plain http would leak the replayed token
		{"ftp://x", false},                  // wrong scheme
		{"not-a-url", false},                // no host
		{"", false},                         // empty
		{"https://", false},                 // no host
	}
	for _, c := range cases {
		err := validateTestURL(c.url)
		if c.ok && err != nil {
			t.Errorf("url %q: unexpected error %v", c.url, err)
		}
		if !c.ok && err == nil {
			t.Errorf("url %q: expected error, got nil", c.url)
		}
	}
}

func TestResolveTestServerSpec_StandaloneRejectsPlainHTTP(t *testing.T) {
	c := newTestCmdForTest()
	_ = c.Flags().Set("url", "http://mcp.example/x") // non-loopback http
	if _, _, err := resolveTestServerSpec(c, "atlassian-read"); err == nil {
		t.Fatal("expected an error: plain http would leak the replayed bearer token")
	}
}
