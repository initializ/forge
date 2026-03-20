package security

import (
	"net/http"
	"testing"
)

func TestSafeRedirectPolicy(t *testing.T) {
	policy := SafeRedirectPolicy(10)

	tests := []struct {
		name            string
		originalURL     string
		redirectURL     string
		wantAuthStrip   bool
		wantCookieStrip bool
	}{
		{
			name:            "same origin preserves headers",
			originalURL:     "https://api.example.com/v1/data",
			redirectURL:     "https://api.example.com/v1/data2",
			wantAuthStrip:   false,
			wantCookieStrip: false,
		},
		{
			name:            "different host strips headers",
			originalURL:     "https://api.example.com/v1/data",
			redirectURL:     "https://evil.com/capture",
			wantAuthStrip:   true,
			wantCookieStrip: true,
		},
		{
			name:            "different scheme strips headers",
			originalURL:     "https://api.example.com/v1/data",
			redirectURL:     "http://api.example.com/v1/data",
			wantAuthStrip:   true,
			wantCookieStrip: true,
		},
		{
			name:            "different port strips headers",
			originalURL:     "https://api.example.com/v1/data",
			redirectURL:     "https://api.example.com:8443/v1/data",
			wantAuthStrip:   true,
			wantCookieStrip: true,
		},
		{
			name:            "implicit port matches explicit 443",
			originalURL:     "https://api.example.com/v1/data",
			redirectURL:     "https://api.example.com:443/v1/data",
			wantAuthStrip:   false,
			wantCookieStrip: false,
		},
		{
			name:            "implicit port matches explicit 80",
			originalURL:     "http://api.example.com/v1/data",
			redirectURL:     "http://api.example.com:80/v1/data",
			wantAuthStrip:   false,
			wantCookieStrip: false,
		},
		{
			name:            "case insensitive host match",
			originalURL:     "https://API.Example.COM/v1/data",
			redirectURL:     "https://api.example.com/v1/data",
			wantAuthStrip:   false,
			wantCookieStrip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original, _ := http.NewRequest("GET", tt.originalURL, nil)
			redirect, _ := http.NewRequest("GET", tt.redirectURL, nil)
			redirect.Header.Set("Authorization", "Bearer secret")
			redirect.Header.Set("Proxy-Authorization", "Basic creds")
			redirect.Header.Set("Cookie", "session=abc")
			redirect.Header.Set("Cookie2", "old=val")

			err := policy(redirect, []*http.Request{original})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			hasAuth := redirect.Header.Get("Authorization") != ""
			hasCookie := redirect.Header.Get("Cookie") != ""

			if tt.wantAuthStrip && hasAuth {
				t.Error("expected Authorization header to be stripped")
			}
			if !tt.wantAuthStrip && !hasAuth {
				t.Error("expected Authorization header to be preserved")
			}
			if tt.wantCookieStrip && hasCookie {
				t.Error("expected Cookie header to be stripped")
			}
			if !tt.wantCookieStrip && !hasCookie {
				t.Error("expected Cookie header to be preserved")
			}

			// Also check Proxy-Authorization and Cookie2
			if tt.wantAuthStrip && redirect.Header.Get("Proxy-Authorization") != "" {
				t.Error("expected Proxy-Authorization header to be stripped")
			}
			if tt.wantCookieStrip && redirect.Header.Get("Cookie2") != "" {
				t.Error("expected Cookie2 header to be stripped")
			}
		})
	}
}

func TestSafeRedirectPolicyMaxRedirects(t *testing.T) {
	policy := SafeRedirectPolicy(2)

	original, _ := http.NewRequest("GET", "https://example.com/1", nil)
	second, _ := http.NewRequest("GET", "https://example.com/2", nil)
	third, _ := http.NewRequest("GET", "https://example.com/3", nil)

	// 2 via requests means we're at redirect #3, which exceeds max of 2
	err := policy(third, []*http.Request{original, second})
	if err == nil {
		t.Error("expected error for exceeding max redirects")
	}
}

func TestIsSameOrigin(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"same", "https://example.com/a", "https://example.com/b", true},
		{"diff host", "https://a.com/x", "https://b.com/x", false},
		{"diff scheme", "https://a.com/x", "http://a.com/x", false},
		{"diff port", "https://a.com/x", "https://a.com:8443/x", false},
		{"implicit 443", "https://a.com/x", "https://a.com:443/x", true},
		{"implicit 80", "http://a.com/x", "http://a.com:80/x", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := http.NewRequest("GET", tt.a, nil)
			b, _ := http.NewRequest("GET", tt.b, nil)
			if got := isSameOrigin(a, b); got != tt.want {
				t.Errorf("isSameOrigin(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
