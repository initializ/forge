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

	sd := NewSafeDialer(resolver, false)
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

	sd := NewSafeDialer(resolver, true)
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
	sd := NewSafeDialer(resolver, true)
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

	sd := NewSafeDialer(resolver, true)
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

	sd := NewSafeDialer(resolver, false)
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

	sd := NewSafeDialer(resolver, false)
	_, err := sd.SafeDialContext(context.Background(), "tcp", "nonexistent.example.com:80")
	if err == nil {
		t.Fatal("expected error for DNS failure")
	}
}

func TestSafeDialerIPLiteral(t *testing.T) {
	sd := NewSafeDialer(nil, false)

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
	sd := NewSafeDialer(nil, false)

	_, err := sd.SafeDialContext(context.Background(), "tcp", "0x7f000001:80")
	if err == nil {
		t.Fatal("expected error for hex IP")
	}
	if !strings.Contains(err.Error(), "non-standard IP") {
		t.Errorf("expected 'non-standard IP' error, got: %v", err)
	}
}

func TestSafeDialerInvalidAddress(t *testing.T) {
	sd := NewSafeDialer(nil, false)

	_, err := sd.SafeDialContext(context.Background(), "tcp", "no-port")
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
}
