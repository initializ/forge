package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// newLoginCmdForTest builds a command carrying the same flags mcpLoginCmd
// registers, so standaloneServerConfig can be exercised without a browser.
func newLoginCmdForTest() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().String("url", "", "")
	c.Flags().String("client-id", "", "")
	c.Flags().StringSlice("scopes", nil, "")
	c.Flags().String("authorize-url", "", "")
	c.Flags().String("token-url", "", "")
	c.Flags().String("token-store-path", "", "")
	return c
}

func TestStandaloneServerConfig_FromFlags(t *testing.T) {
	c := newLoginCmdForTest()
	_ = c.Flags().Set("url", "https://mcp.example/x")
	_ = c.Flags().Set("client-id", "cid")
	_ = c.Flags().Set("scopes", "read:confluence,search")
	_ = c.Flags().Set("token-store-path", "/tmp/creds")

	sc, store, err := standaloneServerConfig(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.ServerURL != "https://mcp.example/x" {
		t.Errorf("ServerURL = %q", sc.ServerURL)
	}
	if sc.ClientID != "cid" {
		t.Errorf("ClientID = %q", sc.ClientID)
	}
	if len(sc.Scopes) != 2 || sc.Scopes[0] != "read:confluence" || sc.Scopes[1] != "search" {
		t.Errorf("Scopes = %v", sc.Scopes)
	}
	if sc.Grant != "" {
		t.Errorf("Grant = %q, want empty (→ authorization_code)", sc.Grant)
	}
	if store != "/tmp/creds" {
		t.Errorf("storePath = %q", store)
	}
}

func TestStandaloneServerConfig_EnvStorePathFallback(t *testing.T) {
	t.Setenv("MCP_TOKEN_STORE_PATH", "/env/creds")
	c := newLoginCmdForTest()
	_ = c.Flags().Set("url", "https://x")

	_, store, err := standaloneServerConfig(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store != "/env/creds" {
		t.Errorf("storePath = %q, want env fallback", store)
	}
}

func TestValidateServerName(t *testing.T) {
	good := []string{"atlassian", "atlassian-read", "a", "a1-b2", "x0"}
	for _, n := range good {
		if err := validateServerName(n); err != nil {
			t.Errorf("validateServerName(%q) = %v, want nil", n, err)
		}
	}
	// Traversal / non-slug names must be rejected so the token never escapes the
	// credential dir (the review's reported gap).
	bad := []string{
		"", "1abc", "Atlassian", "a_b", "a.b",
		"x/../../../../tmp/evil", "a/b", "a\\b", "..", "../x", ".",
		"toolongtoolongtoolongtoolongtoolong", // > 31 chars
	}
	for _, n := range bad {
		if err := validateServerName(n); err == nil {
			t.Errorf("validateServerName(%q) = nil, want error (unsafe name)", n)
		}
	}
}

func TestStandaloneServerConfig_URLValidation(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://mcp.example/x", true},
		{"http://localhost:9000/mcp", true},
		{"ftp://x", false},   // wrong scheme
		{"not-a-url", false}, // no host
		{"", false},          // empty
		{"https://", false},  // no host
		{"://nohost", false}, // malformed
	}
	for _, c := range cases {
		cmd := newLoginCmdForTest()
		_ = cmd.Flags().Set("url", c.url)
		_, _, err := standaloneServerConfig(cmd)
		if c.ok && err != nil {
			t.Errorf("url %q: unexpected error %v", c.url, err)
		}
		if !c.ok && err == nil {
			t.Errorf("url %q: expected error, got nil", c.url)
		}
	}
}

func TestStandaloneServerConfig_EndpointPairing(t *testing.T) {
	c := newLoginCmdForTest()
	_ = c.Flags().Set("url", "https://x")
	_ = c.Flags().Set("authorize-url", "https://a") // token-url intentionally omitted

	if _, _, err := standaloneServerConfig(c); err == nil {
		t.Fatal("expected an error when --authorize-url is set without --token-url")
	}
}
