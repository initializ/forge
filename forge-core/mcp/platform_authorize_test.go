package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/runtime"
)

func TestFetchAuthorizeURL(t *testing.T) {
	t.Run("returns the platform-built URL", func(t *testing.T) {
		var gotBody map[string]string
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"authorize_url": "https://idp.example/authorize?client_id=platform&state=abc"})
		}))
		defer srv.Close()

		url, err := FetchAuthorizeURL(context.Background(), srv.Client(), srv.URL, "agent-cred", "mcp.atlassian", "alice@corp.com")
		if err != nil {
			t.Fatalf("FetchAuthorizeURL: %v", err)
		}
		if url != "https://idp.example/authorize?client_id=platform&state=abc" {
			t.Errorf("url = %q", url)
		}
		if gotBody["server"] != "mcp.atlassian" || gotBody["subject"] != "alice@corp.com" {
			t.Errorf("request body = %+v, want {server, subject}", gotBody)
		}
		if gotAuth != "Bearer agent-cred" {
			t.Errorf("auth header = %q, want Bearer agent-cred", gotAuth)
		}
	})

	t.Run("presents workload token when active", func(t *testing.T) {
		// Per-site wire pin (agent-identity L1, #444, PR #445 review): the
		// authorize-URL callout also carries X-Workload-Token. The SA token
		// goes to the PLATFORM (which returns the third-party consent URL),
		// never to the MCP server.
		tokPath := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokPath, []byte("wl-authz\n"), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		t.Setenv(runtime.EnvWorkloadIdentityMode, runtime.WorkloadIdentityModeK8sSA)
		t.Setenv(runtime.EnvWorkloadTokenPath, tokPath)

		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get(runtime.HeaderWorkloadToken)
			_ = json.NewEncoder(w).Encode(map[string]any{"authorize_url": "https://idp.example/authorize"})
		}))
		defer srv.Close()

		if _, err := FetchAuthorizeURL(context.Background(), srv.Client(), srv.URL, "agent-cred", "mcp.atlassian", "alice@corp.com"); err != nil {
			t.Fatalf("FetchAuthorizeURL: %v", err)
		}
		if got != "wl-authz" {
			t.Errorf("authorize callout %s = %q, want wl-authz", runtime.HeaderWorkloadToken, got)
		}
	})

	t.Run("empty subject is ErrNoToken", func(t *testing.T) {
		if _, err := FetchAuthorizeURL(context.Background(), http.DefaultClient, "https://x", "id", "ref", ""); !errors.Is(err, ErrNoToken) {
			t.Fatalf("empty subject err = %v, want ErrNoToken", err)
		}
	})

	t.Run("non-200 is a protocol error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		if _, err := FetchAuthorizeURL(context.Background(), srv.Client(), srv.URL, "id", "ref", "s"); err == nil {
			t.Fatal("non-200 must error")
		}
	})

	t.Run("missing authorize_url errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()
		if _, err := FetchAuthorizeURL(context.Background(), srv.Client(), srv.URL, "id", "ref", "s"); err == nil {
			t.Fatal("empty authorize_url must error")
		}
	})
}
