package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersPresent(t *testing.T) {
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		method string
	}{
		{"GET"},
		{"POST"},
		{"OPTIONS"},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			expected := map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "no-referrer",
				"X-Frame-Options":         "DENY",
				"Content-Security-Policy": "default-src 'none'",
			}
			for header, want := range expected {
				got := rec.Header().Get(header)
				if got != want {
					t.Errorf("%s %s: header %q = %q, want %q", tt.method, "/", header, got, want)
				}
			}
		})
	}
}

func TestCORSAllowlistMatchedOrigin(t *testing.T) {
	allowed := []string{"http://localhost", "https://app.example.com"}
	middleware := newCORSMiddleware(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name           string
		origin         string
		wantCORS       bool
		wantOriginEcho string
	}{
		{"matching origin", "http://localhost", true, "http://localhost"},
		{"matching with port", "http://localhost:3000", true, "http://localhost:3000"},
		{"matching exact", "https://app.example.com", true, "https://app.example.com"},
		{"non-matching origin", "https://evil.com", false, ""},
		{"no origin header", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			middleware.ServeHTTP(rec, req)

			gotOrigin := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantCORS {
				if gotOrigin != tt.wantOriginEcho {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q", gotOrigin, tt.wantOriginEcho)
				}
				if rec.Header().Get("Vary") != "Origin" {
					t.Error("expected Vary: Origin header")
				}
			} else {
				if gotOrigin != "" {
					t.Errorf("expected no CORS headers, got Access-Control-Allow-Origin = %q", gotOrigin)
				}
			}
		})
	}
}

func TestCORSWildcard(t *testing.T) {
	middleware := newCORSMiddleware([]string{"*"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://anything.com")
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}

func TestCORSPreflight(t *testing.T) {
	middleware := newCORSMiddleware([]string{"http://localhost"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called for OPTIONS")
	}))

	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.Header.Set("Origin", "http://localhost")
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost")
	}
}

func TestDefaultAllowedOrigins(t *testing.T) {
	origins := DefaultAllowedOrigins()
	if len(origins) == 0 {
		t.Fatal("DefaultAllowedOrigins should return at least one origin")
	}

	expected := map[string]bool{
		"http://localhost":  true,
		"https://localhost": true,
		"http://127.0.0.1":  true,
		"https://127.0.0.1": true,
		"http://[::1]":      true,
		"https://[::1]":     true,
	}
	for _, o := range origins {
		if !expected[o] {
			t.Errorf("unexpected origin in defaults: %q", o)
		}
	}
}

func TestIsOriginAllowed(t *testing.T) {
	allowed := []string{"http://localhost", "https://app.example.com"}

	tests := []struct {
		origin string
		want   bool
	}{
		{"http://localhost", true},
		{"http://localhost:3000", true},
		{"https://app.example.com", true},
		{"https://evil.com", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			if got := isOriginAllowed(tt.origin, allowed); got != tt.want {
				t.Errorf("isOriginAllowed(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}

func TestSecurityHeadersOnErrorResponses(t *testing.T) {
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q on 401 response", got)
	}
}
