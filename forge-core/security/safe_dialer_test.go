package security

import (
	"context"
	"net"
	"strings"
	"testing"
)

// mockResolver implements Resolver for testing.
type mockResolver struct {
	addrs map[string][]net.IPAddr
	err   error
}

func (m *mockResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if m.err != nil {
		return nil, m.err
	}
	if addrs, ok := m.addrs[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestSafeDialerBlocksPrivateResolution(t *testing.T) {
	resolver := &mockResolver{
		addrs: map[string][]net.IPAddr{
			"internal.example.com": {{IP: net.ParseIP("10.0.0.1")}},
		},
	}

	sd := NewSafeDialer(resolver, false, nil)
	_, err := sd.SafeDialContext(context.Background(), "tcp", "internal.example.com:80")
	if err == nil {
		t.Fatal("expected error for domain resolving to private IP")
	}
	if !strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("expected 'blocked IP' error, got: %v", err)
	}
}

func TestSafeDialerAllowsPrivateWhenConfigured(t *testing.T) {
	resolver := &mockResolver{
		addrs: map[string][]net.IPAddr{
			"service.cluster.local": {{IP: net.ParseIP("10.96.0.1")}},
		},
	}

	sd := NewSafeDialer(resolver, true, nil)
	// This will fail at the dial stage (no actual service), but should
	// pass IP validation
	_, err := sd.SafeDialContext(context.Background(), "tcp", "service.cluster.local:80")
	if err == nil {
		return // unexpectedly connected
	}
	if strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("should allow private IPs when configured, got: %v", err)
	}
}

func TestSafeDialerBlocksMetadataAlways(t *testing.T) {
	resolver := &mockResolver{
		addrs: map[string][]net.IPAddr{
			"metadata.internal": {{IP: net.ParseIP("169.254.169.254")}},
		},
	}

	// Even with allowPrivate=true, metadata must be blocked
	sd := NewSafeDialer(resolver, true, nil)
	_, err := sd.SafeDialContext(context.Background(), "tcp", "metadata.internal:80")
	if err == nil {
		t.Fatal("expected error for metadata IP")
	}
	if !strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("expected 'blocked IP' error, got: %v", err)
	}
}

func TestSafeDialerBlocksLoopbackAlways(t *testing.T) {
	resolver := &mockResolver{
		addrs: map[string][]net.IPAddr{
			"loopback.example.com": {{IP: net.ParseIP("127.0.0.1")}},
		},
	}

	sd := NewSafeDialer(resolver, true, nil)
	_, err := sd.SafeDialContext(context.Background(), "tcp", "loopback.example.com:80")
	if err == nil {
		t.Fatal("expected error for loopback IP")
	}
	if !strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("expected 'blocked IP' error, got: %v", err)
	}
}

func TestSafeDialerBlocksMixedIPs(t *testing.T) {
	resolver := &mockResolver{
		addrs: map[string][]net.IPAddr{
			"mixed.example.com": {
				{IP: net.ParseIP("8.8.8.8")},
				{IP: net.ParseIP("10.0.0.1")},
			},
		},
	}

	sd := NewSafeDialer(resolver, false, nil)
	_, err := sd.SafeDialContext(context.Background(), "tcp", "mixed.example.com:80")
	if err == nil {
		t.Fatal("expected error when any resolved IP is blocked")
	}
	if !strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("expected 'blocked IP' error, got: %v", err)
	}
}

func TestSafeDialerDNSFailure(t *testing.T) {
	resolver := &mockResolver{
		addrs: map[string][]net.IPAddr{},
	}

	sd := NewSafeDialer(resolver, false, nil)
	_, err := sd.SafeDialContext(context.Background(), "tcp", "nonexistent.example.com:80")
	if err == nil {
		t.Fatal("expected error for DNS failure")
	}
}

func TestSafeDialerIPLiteral(t *testing.T) {
	sd := NewSafeDialer(nil, false, nil)

	// Blocked IP literal
	_, err := sd.SafeDialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatal("expected error for blocked IP literal")
	}
	if !strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("expected 'blocked IP' error, got: %v", err)
	}
}

func TestSafeDialerRejectsNonStandardIP(t *testing.T) {
	sd := NewSafeDialer(nil, false, nil)

	_, err := sd.SafeDialContext(context.Background(), "tcp", "0x7f000001:80")
	if err == nil {
		t.Fatal("expected error for hex IP")
	}
	if !strings.Contains(err.Error(), "non-standard IP") {
		t.Errorf("expected 'non-standard IP' error, got: %v", err)
	}
}

func TestSafeDialerInvalidAddress(t *testing.T) {
	sd := NewSafeDialer(nil, false, nil)

	_, err := sd.SafeDialContext(context.Background(), "tcp", "no-port")
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
}

// TestSafeDialer_PrivateCIDRAllowlist covers the vend-time integration:
// a hostname resolving into a private CIDR listed under allowedPrivateCIDRs
// should pass the IP check (dial still fails on unreachable target, which
// is fine — we only assert we get past the block, not that the connection
// succeeds).
func TestSafeDialer_PrivateCIDRAllowlist(t *testing.T) {
	cidrs, err := ParsePrivateCIDRs([]string{"10.20.0.0/16"})
	if err != nil {
		t.Fatalf("ParsePrivateCIDRs: %v", err)
	}

	t.Run("resolved IP inside listed CIDR passes IP check", func(t *testing.T) {
		resolver := &mockResolver{
			addrs: map[string][]net.IPAddr{
				"db.internal": {{IP: net.ParseIP("10.20.0.5")}},
			},
		}
		sd := NewSafeDialer(resolver, false, cidrs)
		_, err := sd.SafeDialContext(context.Background(), "tcp", "db.internal:5432")
		// Dial will fail (nothing listening at 10.20.0.5), but the failure
		// must not be from the IP block — that's the invariant we care about.
		if err != nil && strings.Contains(err.Error(), "blocked IP") {
			t.Errorf("CIDR-listed private IP should pass IP check, got: %v", err)
		}
	})

	t.Run("resolved IP outside listed CIDR stays blocked", func(t *testing.T) {
		resolver := &mockResolver{
			addrs: map[string][]net.IPAddr{
				"other.internal": {{IP: net.ParseIP("10.99.0.5")}},
			},
		}
		sd := NewSafeDialer(resolver, false, cidrs)
		_, err := sd.SafeDialContext(context.Background(), "tcp", "other.internal:5432")
		if err == nil {
			t.Fatal("expected block for private IP outside CIDR list")
		}
		if !strings.Contains(err.Error(), "blocked IP") {
			t.Errorf("expected 'blocked IP' error, got: %v", err)
		}
	})

	t.Run("cloud metadata cannot be opened via CIDR list", func(t *testing.T) {
		// Even if an operator lists 169.254.0.0/16, metadata IP stays blocked.
		metadataCIDRs, err := ParsePrivateCIDRs([]string{"169.254.0.0/16"})
		if err != nil {
			t.Fatalf("ParsePrivateCIDRs: %v", err)
		}
		resolver := &mockResolver{
			addrs: map[string][]net.IPAddr{
				"metadata": {{IP: net.ParseIP("169.254.169.254")}},
			},
		}
		sd := NewSafeDialer(resolver, false, metadataCIDRs)
		_, err = sd.SafeDialContext(context.Background(), "tcp", "metadata:80")
		if err == nil {
			t.Fatal("metadata IP must NEVER be reachable via CIDR list")
		}
		if !strings.Contains(err.Error(), "blocked IP") {
			t.Errorf("expected 'blocked IP' error, got: %v", err)
		}
	})

	t.Run("IP literal inside listed CIDR passes IP check", func(t *testing.T) {
		// This exercises the ParseIP path (no DNS lookup).
		sd := NewSafeDialer(nil, false, cidrs)
		_, err := sd.SafeDialContext(context.Background(), "tcp", "10.20.0.5:5432")
		if err != nil && strings.Contains(err.Error(), "blocked IP") {
			t.Errorf("CIDR-listed private IP literal should pass, got: %v", err)
		}
	})
}
