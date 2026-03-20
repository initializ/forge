package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/initializ/forge/forge-core/security"
)

// Policy represents the egress policy loaded from egress_allowlist.json
type Policy struct {
	Mode            security.EgressMode `json:"mode"`
	AllowedDomains  []string             `json:"allowed_domains"`
	AllowPrivateIPs bool                 `json:"allow_private_ips"`
}

// LoadPolicy loads the egress policy from the specified JSON file.
func LoadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}

	var policy Policy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}

	// Validate mode
	switch policy.Mode {
	case security.ModeAllowlist, security.ModeDenyAll, security.ModeDevOpen:
		// Valid
	default:
		return nil, fmt.Errorf("invalid egress mode: %q", policy.Mode)
	}

	return &policy, nil
}
