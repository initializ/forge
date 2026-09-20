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
