package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
