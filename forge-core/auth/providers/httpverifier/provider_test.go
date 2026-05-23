package httpverifier_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/auth/providers/httpverifier"
)

// fakeVerifier returns a test server that implements the verifier contract.
// `handler` lets each test customize the response.
func fakeVerifier(t *testing.T, handler func(req map[string]any) (status int, body any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("verifier got method %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("verifier got Content-Type %q, want application/json", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("verifier got non-JSON body: %v", err)
		}
		status, respBody := handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if respBody != nil {
			_ = json.NewEncoder(w).Encode(respBody)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNew_ValidationErrors(t *testing.T) {
	t.Run("missing url", func(t *testing.T) {
		_, err := httpverifier.New(httpverifier.Config{})
		if !errors.Is(err, auth.ErrProviderNotConfigured) {
			t.Fatalf("err = %v, want ErrProviderNotConfigured", err)
		}
	})
}

func TestVerify_HappyPath(t *testing.T) {
	srv := fakeVerifier(t, func(req map[string]any) (int, any) {
		if req["token"] != "good-token" {
			t.Errorf("verifier got token %q, want good-token", req["token"])
		}
		return http.StatusOK, map[string]any{
			"valid":        true,
			"user_id":      "user-1",
			"org_id":       "org-1",
			"email":        "user@example.com",
			"workspace_id": "ws-1",
		}
	})

	p, err := httpverifier.New(httpverifier.Config{URL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := p.Verify(context.Background(), "good-token", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id == nil {
		t.Fatal("Verify returned nil identity")
	}
	if id.UserID != "user-1" || id.Email != "user@example.com" || id.OrgID != "org-1" || id.WorkspaceID != "ws-1" {
		t.Errorf("identity = %+v, fields missing", id)
	}
	if id.Source != httpverifier.ProviderName {
		t.Errorf("identity.Source = %q, want %q", id.Source, httpverifier.ProviderName)
	}
}

func TestVerify_ValidFalse_ReturnsRejected(t *testing.T) {
	srv := fakeVerifier(t, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"valid": false, "error": "bad token"}
	})

	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
	_, err := p.Verify(context.Background(), "bad-token", nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected", err)
	}
}

func TestVerify_401_ReturnsRejected(t *testing.T) {
	srv := fakeVerifier(t, func(map[string]any) (int, any) {
		return http.StatusUnauthorized, nil
	})

	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
	_, err := p.Verify(context.Background(), "x", nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected", err)
	}
}

func TestVerify_500_ReturnsProviderUnavailable(t *testing.T) {
	// Review #6: 5xx is the verifier's fault, not the token's. Distinct
	// sentinel so operators triaging audit logs don't chase token issues
	// when the actual problem is verifier downtime.
	srv := fakeVerifier(t, func(map[string]any) (int, any) {
		return http.StatusInternalServerError, map[string]any{"error": "boom"}
	})

	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
	_, err := p.Verify(context.Background(), "x", nil)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err also matched ErrInvalidToken — sentinels must not overlap: %v", err)
	}
}

func TestVerify_502BadGateway_ReturnsProviderUnavailable(t *testing.T) {
	// Other 5xx codes — same classification.
	srv := fakeVerifier(t, func(map[string]any) (int, any) {
		return http.StatusBadGateway, nil
	})
	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
	_, err := p.Verify(context.Background(), "x", nil)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestVerify_NetworkError_ReturnsProviderUnavailable(t *testing.T) {
	p, _ := httpverifier.New(httpverifier.Config{
		URL:     "http://127.0.0.1:0/never", // port 0 is invalid, will fail to connect
		Timeout: 100 * time.Millisecond,
	})
	_, err := p.Verify(context.Background(), "x", nil)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestVerify_Non401_4xx_ReturnsRejected(t *testing.T) {
	// 4xx other than 401 (e.g., 400, 403) means the verifier
	// explicitly refused the request — token-side, not server-side.
	for _, code := range []int{http.StatusBadRequest, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := fakeVerifier(t, func(map[string]any) (int, any) { return code, nil })
			p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
			_, err := p.Verify(context.Background(), "x", nil)
			if !errors.Is(err, auth.ErrTokenRejected) {
				t.Fatalf("status %d → err = %v, want ErrTokenRejected", code, err)
			}
		})
	}
}

func TestVerify_UndecodableBody_ReturnsProviderUnavailable(t *testing.T) {
	// 200 OK but body isn't the contract JSON — verifier misbehavior,
	// not a token issue.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not even close to JSON {{{}"))
	}))
	defer srv.Close()

	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
	_, err := p.Verify(context.Background(), "x", nil)
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestVerify_OrgID_Precedence(t *testing.T) {
	tests := []struct {
		name    string
		headers auth.Headers
		want    string
	}{
		{
			name:    "header X-Org-ID wins",
			headers: auth.Headers{"X-Org-ID": "header-org", "org-id": "lower-org"},
			want:    "header-org",
		},
		{
			name:    "lowercase org-id when X-Org-ID absent",
			headers: auth.Headers{"org-id": "lower-org"},
			want:    "lower-org",
		},
		{
			name:    "snake_case org_id when others absent",
			headers: auth.Headers{"org_id": "snake-org"},
			want:    "snake-org",
		},
		{
			name:    "config default when no headers",
			headers: auth.Headers{},
			want:    "default-org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotOrgID atomic.Value
			srv := fakeVerifier(t, func(req map[string]any) (int, any) {
				gotOrgID.Store(req["org_id"])
				return http.StatusOK, map[string]any{"valid": true, "user_id": "u"}
			})

			p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL, DefaultOrg: "default-org"})
			if _, err := p.Verify(context.Background(), "tok", tt.headers); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got := gotOrgID.Load(); got != tt.want {
				t.Errorf("verifier received org_id = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestVerify_WireFormat_PreservedExactly(t *testing.T) {
	// Black-box check: the JSON body sent must contain exactly "token" and
	// "org_id" fields — no more, no less. This guards against accidental
	// schema drift from the legacy --auth-url contract.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		if len(got) != 2 {
			t.Errorf("request body has %d fields, want 2: %v", len(got), got)
		}
		if _, ok := got["token"]; !ok {
			t.Error("request body missing 'token' field")
		}
		if _, ok := got["org_id"]; !ok {
			t.Error("request body missing 'org_id' field")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true})
	}))
	defer srv.Close()

	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL})
	if _, err := p.Verify(context.Background(), "tok", nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_RespectsContextCancellation(t *testing.T) {
	// Server blocks until either the request context is cancelled OR the
	// test ends. The serverDone channel guarantees srv.Close() doesn't
	// hang waiting for the handler to return under -race scheduling.
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-serverDone:
		}
	}))
	defer func() {
		close(serverDone)
		srv.Close()
	}()

	p, _ := httpverifier.New(httpverifier.Config{URL: srv.URL, Timeout: 5 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Verify(ctx, "tok", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Verify returned nil error despite context timeout")
	}
	if elapsed > 1*time.Second {
		t.Errorf("Verify took %v, expected <1s (context should have cancelled)", elapsed)
	}
}

func TestRegisteredViaFactory(t *testing.T) {
	// Confirm the package registered itself with the auth package on import.
	p, err := auth.Build("http_verifier", map[string]any{"url": "https://example.com/verify"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "http_verifier" {
		t.Errorf("Name = %q, want http_verifier", p.Name())
	}
}

func TestFactory_UnknownSettings_AreIgnored(t *testing.T) {
	// Forward-compat: unknown YAML keys must not break construction.
	_, err := auth.Build("http_verifier", map[string]any{
		"url":           "https://example.com/verify",
		"unknown_field": "future",
	})
	if err != nil {
		t.Fatalf("Build with unknown field: %v", err)
	}
}
