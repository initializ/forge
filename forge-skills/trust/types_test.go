package trust

import (
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestDefaultTrustPolicy_RequiresChecksum(t *testing.T) {
	p := DefaultTrustPolicy()
	if !p.RequireChecksum {
		t.Error("DefaultTrustPolicy().RequireChecksum = false, want true")
	}
}

func TestDefaultTrustPolicy_DoesNotRequireSignature(t *testing.T) {
	p := DefaultTrustPolicy()
	if p.RequireSignature {
		t.Error("DefaultTrustPolicy().RequireSignature = true, want false")
	}
}

func TestDefaultTrustPolicy_AcceptsLocal(t *testing.T) {
	p := DefaultTrustPolicy()
	if !p.Accepts(contract.TrustLocal) {
		t.Error("DefaultTrustPolicy should accept TrustLocal")
	}
}
