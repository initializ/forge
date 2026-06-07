package types

import (
	"testing"
)

// FWS-10 / issue #110: forge.yaml round-trip of the server.rate_limit
// block. Locks the YAML tag names so a future rename doesn't silently
// break operator configs.
func TestParseForgeConfig_ServerRateLimitRoundTrip(t *testing.T) {
	yaml := `
agent_id: rl-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
server:
  rate_limit:
    read_rps: 0.5
    read_burst: 5
    write_rps: 2.0
    write_burst: 50
    cancel_exempt: false
`
	cfg, err := ParseForgeConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rl := cfg.Server.RateLimit
	if rl.ReadRPS != 0.5 {
		t.Errorf("ReadRPS = %v, want 0.5", rl.ReadRPS)
	}
	if rl.ReadBurst != 5 {
		t.Errorf("ReadBurst = %d, want 5", rl.ReadBurst)
	}
	if rl.WriteRPS != 2.0 {
		t.Errorf("WriteRPS = %v, want 2.0", rl.WriteRPS)
	}
	if rl.WriteBurst != 50 {
		t.Errorf("WriteBurst = %d, want 50", rl.WriteBurst)
	}
	if rl.CancelExempt == nil {
		t.Fatal("CancelExempt should be non-nil (explicitly set to false)")
	}
	if *rl.CancelExempt {
		t.Errorf("CancelExempt = true, want false (yaml said false)")
	}
}

// TestParseForgeConfig_ServerBlockOptional: omitting `server:` is
// fully backward compatible — every rate_limit field stays at the
// zero value, signaling "use the runtime defaults".
func TestParseForgeConfig_ServerBlockOptional(t *testing.T) {
	yaml := `
agent_id: noserver
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
`
	cfg, err := ParseForgeConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rl := cfg.Server.RateLimit
	if rl.ReadRPS != 0 || rl.WriteRPS != 0 || rl.ReadBurst != 0 || rl.WriteBurst != 0 {
		t.Errorf("expected zero-value rate_limit, got %+v", rl)
	}
	if rl.CancelExempt != nil {
		t.Errorf("expected nil CancelExempt (unset), got %v", *rl.CancelExempt)
	}
}
