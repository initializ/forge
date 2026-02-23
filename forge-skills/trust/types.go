// Package trust provides integrity verification, signature handling, and trust
// policy enforcement for forge skills.
package trust

import "github.com/initializ/forge/forge-skills/contract"

// TrustPolicy defines minimum trust requirements for skill loading.
type TrustPolicy struct {
	MinTrustLevel    contract.TrustLevel `yaml:"min_trust_level" json:"min_trust_level"`
	RequireChecksum  bool                `yaml:"require_checksum" json:"require_checksum"`
	RequireSignature bool                `yaml:"require_signature" json:"require_signature"`
}

// DefaultTrustPolicy returns a policy that accepts local skills without signatures.
func DefaultTrustPolicy() TrustPolicy {
	return TrustPolicy{
		MinTrustLevel:    contract.TrustLocal,
		RequireChecksum:  false,
		RequireSignature: false,
	}
}

// Accepts reports whether the given trust level satisfies the policy.
func (p TrustPolicy) Accepts(level contract.TrustLevel) bool {
	return trustOrd(level) >= trustOrd(p.MinTrustLevel)
}

// trustOrd returns a numeric ordering for trust levels (higher = more trusted).
func trustOrd(t contract.TrustLevel) int {
	switch t {
	case contract.TrustBuiltin:
		return 3
	case contract.TrustVerified:
		return 2
	case contract.TrustLocal:
		return 1
	case contract.TrustUntrusted:
		return 0
	default:
		return -1
	}
}
