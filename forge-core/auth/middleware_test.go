package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMiddleware(t *testing.T) {
	const validToken = "test-secret-token"

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	tests := []struct {
		name       string
		cfg        Config
		method     string
		path       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "disabled passes through",
			cfg:        Config{Enabled: false},
			method:     "POST",
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name: "valid token accepted",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "POST",
			path:       "/",
			authHeader: "Bearer " + validToken,
			wantStatus: http.StatusOK,
		},
		{
			name: "missing token rejected",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "POST",
			path:       "/",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong token rejected",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "POST",
			path:       "/",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "GET / is public",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "GET",
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name: "GET /.well-known/agent.json is public",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "GET",
			path:       "/.well-known/agent.json",
			wantStatus: http.StatusOK,
		},
		{
			name: "GET /healthz is public",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "GET",
			path:       "/healthz",
			wantStatus: http.StatusOK,
		},
		{
			name: "POST /tasks/send requires auth",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "POST",
			path:       "/tasks/send",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "case insensitive Bearer prefix",
			cfg: Config{
				Enabled:   true,
				Token:     validToken,
				SkipPaths: DefaultSkipPaths(),
			},
			method:     "POST",
			path:       "/",
			authHeader: "bearer " + validToken,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := Middleware(tt.cfg)
			handler := mw(okHandler)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			// Verify JSON error body on 401.
			if tt.wantStatus == http.StatusUnauthorized {
				var resp errorResponse
				if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode error response: %v", err)
				}
				if resp.Error != "unauthorized" {
					t.Errorf("error = %q, want %q", resp.Error, "unauthorized")
				}
			}
		})
	}
}

func TestMiddlewareOnAuthCallback(t *testing.T) {
	const token = "callback-token"

	var successCount, failCount atomic.Int32

	cfg := Config{
		Enabled:   true,
		Token:     token,
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(r *http.Request, success bool) {
			if success {
				successCount.Add(1)
			} else {
				failCount.Add(1)
			}
		},
	}

	handler := Middleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Successful auth.
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// Failed auth.
	req2 := httptest.NewRequest("POST", "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	if got := successCount.Load(); got != 1 {
		t.Errorf("success callbacks = %d, want 1", got)
	}
	if got := failCount.Load(); got != 1 {
		t.Errorf("failure callbacks = %d, want 1", got)
	}
}
