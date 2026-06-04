package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
)

// Regression tests for issue #85 / FWS-1 — HTTP behavior of the
// Agent Card endpoint. The canonical path is the A2A 0.3.0
// /.well-known/agent-card.json; the legacy /.well-known/agent.json
// remains served with a Deprecation header for one release cycle.

// startTestServer boots a Server on a random port and returns the base
// URL plus a cancel function. The test card is minimal but spec-shaped.
func startTestServer(t *testing.T) (string, context.CancelFunc) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	card := &a2a.AgentCard{
		Name:               "test-agent",
		Description:        "Test agent for issue #85",
		URL:                "http://127.0.0.1",
		Version:            "0.1.0",
		ProtocolVersion:    "0.3.0",
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.Skill{
			{ID: "echo", Name: "Echo", Description: "Echoes the input", Tags: []string{"test"}},
		},
	}
	srv := NewServer(ServerConfig{
		Port:            port,
		Host:            "127.0.0.1",
		AgentCard:       card,
		ShutdownTimeout: 1 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Start(ctx) }()

	// Wait briefly for the listener to come up. We dial at the port we
	// picked above — Server.Start performs its own port-conflict
	// retry, but reading s.port concurrently with Start is racy so we
	// trust the freshly-released port to be reused.
	addr := "127.0.0.1:" + itoa(port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			return "http://" + addr, cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("server failed to come up")
	return "", cancel
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestAgentCard_CanonicalPathReturnsCard(t *testing.T) {
	base, cancel := startTestServer(t)
	defer cancel()

	resp, err := http.Get(base + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Canonical path must NOT carry the Deprecation header.
	if dep := resp.Header.Get("Deprecation"); dep != "" {
		t.Errorf("canonical path should not be deprecated, got Deprecation=%q", dep)
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if card.Name != "test-agent" {
		t.Errorf("Name = %q, want test-agent", card.Name)
	}
	if card.ProtocolVersion != "0.3.0" {
		t.Errorf("ProtocolVersion = %q, want 0.3.0", card.ProtocolVersion)
	}
}

func TestAgentCard_LegacyPathReturnsCardWithDeprecationHeader(t *testing.T) {
	base, cancel := startTestServer(t)
	defer cancel()

	resp, err := http.Get(base + "/.well-known/agent.json")
	if err != nil {
		t.Fatalf("GET legacy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if dep := resp.Header.Get("Deprecation"); dep != "true" {
		t.Errorf("Deprecation header = %q, want %q", dep, "true")
	}
	if link := resp.Header.Get("Link"); link == "" {
		t.Errorf("Link header should point at successor path; got empty")
	} else if !contains(link, "/.well-known/agent-card.json") {
		t.Errorf("Link header should reference canonical path, got %q", link)
	}
}

func TestAgentCard_BothPathsServeIdenticalBody(t *testing.T) {
	base, cancel := startTestServer(t)
	defer cancel()

	canonical := mustGetBody(t, base+"/.well-known/agent-card.json")
	legacy := mustGetBody(t, base+"/.well-known/agent.json")

	if string(canonical) != string(legacy) {
		t.Errorf("canonical and legacy paths should serve identical bytes:\ncanonical=%s\nlegacy=%s",
			canonical, legacy)
	}
}

func mustGetBody(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 0, 1024)
	buf := make([]byte, 512)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return body
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
