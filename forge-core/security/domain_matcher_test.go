package security

import "testing"

func TestDomainMatcherAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		domains []string
		host    string
		allowed bool
	}{
		{
			name:    "exact match",
			domains: []string{"api.openai.com"},
			host:    "api.openai.com",
			allowed: true,
		},
		{
			name:    "exact match blocked",
			domains: []string{"api.openai.com"},
			host:    "evil.com",
			allowed: false,
		},
		{
			name:    "wildcard match",
			domains: []string{"*.github.com"},
			host:    "api.github.com",
			allowed: true,
		},
		{
			name:    "wildcard does not match bare domain",
			domains: []string{"*.github.com"},
			host:    "github.com",
			allowed: false,
		},
		{
			name:    "case insensitive",
			domains: []string{"api.openai.com"},
			host:    "API.OpenAI.COM",
			allowed: true,
		},
		{
			name:    "empty domains blocks all",
			domains: []string{},
			host:    "example.com",
			allowed: false,
		},
		{
			name:    "whitespace trimmed",
			domains: []string{"  api.openai.com  "},
			host:    "api.openai.com",
			allowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDomainMatcher(ModeAllowlist, tt.domains)
			if got := m.IsAllowed(tt.host); got != tt.allowed {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.host, got, tt.allowed)
			}
		})
	}
}

func TestDomainMatcherDevOpen(t *testing.T) {
	m := NewDomainMatcher(ModeDevOpen, nil)
	if !m.IsAllowed("anything.com") {
		t.Error("dev-open should allow everything")
	}
}

func TestDomainMatcherDenyAll(t *testing.T) {
	m := NewDomainMatcher(ModeDenyAll, []string{"api.openai.com"})
	if m.IsAllowed("api.openai.com") {
		t.Error("deny-all should block everything")
	}
}

func TestDomainMatcherMode(t *testing.T) {
	m := NewDomainMatcher(ModeAllowlist, nil)
	if m.Mode() != ModeAllowlist {
		t.Errorf("Mode() = %v, want %v", m.Mode(), ModeAllowlist)
	}
}

func TestIsLocalhostExported(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"example.com", false},
		{"192.168.1.1", false},
		{"10.0.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := IsLocalhost(tt.host); got != tt.expected {
				t.Errorf("IsLocalhost(%q) = %v, want %v", tt.host, got, tt.expected)
			}
		})
	}
}
