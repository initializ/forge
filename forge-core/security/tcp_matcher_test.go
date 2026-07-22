package security

import (
	"strings"
	"testing"
)

func TestTCPMatcher_ExactHostPort(t *testing.T) {
	m, err := NewTCPMatcher([]string{"db.internal:5432"})
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	if !m.IsAllowed("db.internal", "5432") {
		t.Error("exact match should be allowed")
	}
	if m.IsAllowed("db.internal", "5433") {
		t.Error("different port on same host must be denied — port granularity is the point")
	}
	if m.IsAllowed("other.internal", "5432") {
		t.Error("different host on same port must be denied")
	}
}

func TestTCPMatcher_CaseInsensitiveHost(t *testing.T) {
	m, err := NewTCPMatcher([]string{"DB.Internal:5432"})
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	if !m.IsAllowed("db.internal", "5432") {
		t.Error("case-insensitive host match expected")
	}
	if !m.IsAllowed("DB.INTERNAL", "5432") {
		t.Error("case-insensitive host match expected (input uppercase)")
	}
}

func TestTCPMatcher_WildcardHostExactPort(t *testing.T) {
	m, err := NewTCPMatcher([]string{"*.brokers.internal:9092"})
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	if !m.IsAllowed("broker1.brokers.internal", "9092") {
		t.Error("wildcard suffix should match")
	}
	if !m.IsAllowed("cluster-a.brokers.internal", "9092") {
		t.Error("wildcard suffix should match deeper")
	}
	if m.IsAllowed("brokers.internal", "9092") {
		t.Error("bare parent domain must NOT match *.brokers.internal (parity with DomainMatcher)")
	}
	if m.IsAllowed("broker1.brokers.internal", "9093") {
		t.Error("port must match exactly for wildcard entries too")
	}
}

func TestTCPMatcher_AnyPort(t *testing.T) {
	m, err := NewTCPMatcher([]string{"db.internal:*"})
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	if !m.IsAllowed("db.internal", "5432") {
		t.Error("host:* should match any port")
	}
	if !m.IsAllowed("db.internal", "65535") {
		t.Error("host:* should match high port")
	}
	if m.IsAllowed("other.internal", "5432") {
		t.Error("different host must not match")
	}
}

func TestTCPMatcher_WildcardHostAnyPort(t *testing.T) {
	m, err := NewTCPMatcher([]string{"*.internal:*"})
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	if !m.IsAllowed("db.internal", "5432") {
		t.Error("*.internal:* should match db.internal:5432")
	}
	if !m.IsAllowed("broker1.brokers.internal", "9092") {
		t.Error("*.internal:* should match broker1.brokers.internal:9092")
	}
	if m.IsAllowed("external.com", "5432") {
		t.Error("*.internal:* must not match external.com")
	}
}

func TestTCPMatcher_Empty(t *testing.T) {
	m, err := NewTCPMatcher(nil)
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	if !m.Empty() {
		t.Error("nil entries → Empty() true")
	}
	if m.IsAllowed("any", "80") {
		t.Error("empty matcher denies everything")
	}

	// Nil pointer receiver must be safe — callers pass nil when TCPMatcher
	// isn't configured (SetTCPMatcher never called).
	var nilM *TCPMatcher
	if !nilM.Empty() {
		t.Error("nil matcher → Empty() true")
	}
	if nilM.IsAllowed("db.internal", "5432") {
		t.Error("nil matcher must deny (fail closed)")
	}
}

// TestTCPMatcher_IPv6Literal pins the fix from the #355 review:
// bracketed IPv6 literals in the config must round-trip to the
// unbracketed form that the SOCKS5 IPv6 ATYP path produces at runtime
// (`net.IP(buf).String()` → `::1`, no brackets). Pre-fix the entry was
// stored with brackets and never matched, silently denying legitimate
// IPv6 targets.
func TestTCPMatcher_IPv6Literal(t *testing.T) {
	t.Run("bracketed loopback IPv6 matches unbracketed runtime form", func(t *testing.T) {
		m, err := NewTCPMatcher([]string{"[::1]:5432"})
		if err != nil {
			t.Fatalf("NewTCPMatcher: %v", err)
		}
		if !m.IsAllowed("::1", "5432") {
			t.Error("[::1]:5432 should match runtime host ::1 on port 5432")
		}
		if m.IsAllowed("[::1]", "5432") {
			t.Error("bracketed host at runtime must not match — SOCKS5 never produces brackets")
		}
		if m.IsAllowed("::1", "5433") {
			t.Error("port granularity holds for IPv6 targets too")
		}
	})

	t.Run("bracketed IPv6 with any-port", func(t *testing.T) {
		m, err := NewTCPMatcher([]string{"[2001:db8::1]:*"})
		if err != nil {
			t.Fatalf("NewTCPMatcher: %v", err)
		}
		if !m.IsAllowed("2001:db8::1", "5432") {
			t.Error("bracketed IPv6 with :* should match any port")
		}
	})
}

func TestTCPMatcher_InvalidEntries(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{"no port", "db.internal"},
		{"empty host", ":5432"},
		{"empty port", "db.internal:"},
		{"non-numeric port", "db.internal:pgsql"},
		{"port zero", "db.internal:0"},
		{"port too high", "db.internal:99999"},
		{"port negative", "db.internal:-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTCPMatcher([]string{tc.entry})
			if err == nil {
				t.Fatalf("expected error for %q", tc.entry)
			}
			if !strings.Contains(err.Error(), tc.entry) {
				t.Errorf("error should name the bad entry %q, got: %v", tc.entry, err)
			}
		})
	}
}

func TestTCPMatcher_MultipleEntries(t *testing.T) {
	m, err := NewTCPMatcher([]string{
		"db.internal:5432",
		"redis.internal:6379",
		"*.brokers.internal:9092",
		"metrics.internal:*",
	})
	if err != nil {
		t.Fatalf("NewTCPMatcher: %v", err)
	}
	cases := []struct {
		host, port string
		allowed    bool
	}{
		{"db.internal", "5432", true},
		{"db.internal", "5433", false},
		{"redis.internal", "6379", true},
		{"broker1.brokers.internal", "9092", true},
		{"broker1.brokers.internal", "9091", false},
		{"metrics.internal", "9090", true},
		{"metrics.internal", "3000", true},
		{"outside.example.com", "5432", false},
	}
	for _, tc := range cases {
		if got := m.IsAllowed(tc.host, tc.port); got != tc.allowed {
			t.Errorf("IsAllowed(%q, %q) = %v, want %v", tc.host, tc.port, got, tc.allowed)
		}
	}
}
