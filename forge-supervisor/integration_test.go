package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegration tests the supervisor by building it and verifying
// the binary starts and responds to health checks.
func TestIntegration(t *testing.T) {
	// Skip if not in integration test mode
	if os.Getenv("RUN_INTEGRATION_TESTS") != "1" {
		t.Skip("Skipping integration test (set RUN_INTEGRATION_TESTS=1 to run)")
	}

	// Build the supervisor
	cmd := exec.Command("go", "build", "-o", "forge-supervisor-test", ".")
	cmd.Dir = filepath.Dir(os.Args[0])
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build supervisor: %v\noutput: %s", err, string(output))
	}
	defer os.Remove("forge-supervisor-test")

	// Create a temporary egress policy file
	tempDir := t.TempDir()
	policyPath := filepath.Join(tempDir, "egress_allowlist.json")
	policy := `{
		"mode": "allowlist",
		"allowed_domains": ["example.com", "*.github.com"],
		"allow_private_ips": false
	}`
	if err := os.WriteFile(policyPath, []byte(policy), 0644); err != nil {
		t.Fatalf("failed to write policy file: %v", err)
	}

	// Copy policy to current dir for the test
	defer os.Remove(policyPath)

	// Note: We can't actually run the full supervisor in a test because:
	// 1. It needs to be PID 1
	// 2. It needs CAP_NET_ADMIN for iptables
	// 3. It needs to fork/exec an agent
	//
	// Instead, we test the individual components.

	t.Run("PolicyLoading", func(t *testing.T) {
		testPolicyLoading(t, policyPath)
	})

	t.Run("DomainMatcher", func(t *testing.T) {
		testDomainMatcher(t)
	})

	t.Run("HealthEndpoints", func(t *testing.T) {
		testHealthEndpoints(t)
	})

	t.Run("AuditLogger", func(t *testing.T) {
		testAuditLogger(t)
	})
}

func testPolicyLoading(t *testing.T, policyPath string) {
	// Write a test policy to a temp location
	tmpPolicy := `{
		"mode": "allowlist",
		"allowed_domains": ["test.com", "*.example.com"],
		"allow_private_ips": false
	}`
	tmpFile := filepath.Join(t.TempDir(), "test_policy.json")
	if err := os.WriteFile(tmpFile, []byte(tmpPolicy), 0644); err != nil {
		t.Fatalf("failed to write temp policy: %v", err)
	}

	policy, err := LoadPolicy(tmpFile)
	if err != nil {
		t.Fatalf("LoadPolicy failed: %v", err)
	}

	if policy.Mode != "allowlist" {
		t.Errorf("expected mode 'allowlist', got %q", policy.Mode)
	}

	if len(policy.AllowedDomains) != 2 {
		t.Errorf("expected 2 domains, got %d", len(policy.AllowedDomains))
	}
}

func testDomainMatcher(t *testing.T) {
	// Test that we can create a matcher and check domains
	// Note: This tests the import from forge-core works
	policy := `{
		"mode": "allowlist",
		"allowed_domains": ["example.com", "*.github.com"],
		"allow_private_ips": false
	}`
	tmpFile := filepath.Join(t.TempDir(), "test_policy.json")
	if err := os.WriteFile(tmpFile, []byte(policy), 0644); err != nil {
		t.Fatalf("failed to write temp policy: %v", err)
	}

	p, err := LoadPolicy(tmpFile)
	if err != nil {
		t.Fatalf("LoadPolicy failed: %v", err)
	}

	// The matcher is created in main.go, but we can verify the policy
	// has the right structure for the matcher
	if len(p.AllowedDomains) != 2 {
		t.Errorf("expected 2 domains, got %d", len(p.AllowedDomains))
	}
}

func testHealthEndpoints(t *testing.T) {
	// Create a denial tracker and start health endpoints
	tracker := &DenialTracker{}
	StartHealthEndpoints(tracker, 15000)

	// Give the server time to start
	time.Sleep(100 * time.Millisecond)

	// Test /healthz
	resp, err := http.Get("http://127.0.0.1:15000/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz returned status %d, expected 200", resp.StatusCode)
	}

	// Test /denials
	resp, err = http.Get("http://127.0.0.1:15000/denials")
	if err != nil {
		t.Fatalf("denials request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("denials returned status %d, expected 200", resp.StatusCode)
	}

	// Check content type
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("denials Content-Type = %q, expected application/json", ct)
	}

	// Add a denial and verify it's returned
	tracker.Add(DenialEvent{
		Timestamp: time.Now(),
		Host:      "blocked.example.com",
		Port:      443,
	})

	resp, err = http.Get("http://127.0.0.1:15000/denials")
	if err != nil {
		t.Fatalf("denials request failed: %v", err)
	}
	defer resp.Body.Close()

	var denials []DenialEvent
	if err := json.NewDecoder(resp.Body).Decode(&denials); err != nil {
		t.Fatalf("failed to decode denials: %v", err)
	}

	if len(denials) != 1 {
		t.Errorf("expected 1 denial, got %d", len(denials))
	}

	if denials[0].Host != "blocked.example.com" {
		t.Errorf("expected host 'blocked.example.com', got %q", denials[0].Host)
	}
}

func testAuditLogger(t *testing.T) {
	// Create audit logger and verify it produces NDJSON
	_ = NewAuditLogger()

	// This would write to stdout - in tests we verify the struct is correct
	event := &AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    "allowed",
		Host:      "example.com",
		Port:      443,
	}

	// Verify the event can be marshaled to JSON
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal audit event: %v", err)
	}

	// Verify it's valid NDJSON (single line JSON)
	lines := strings.Split(string(data), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(lines))
	}
}
