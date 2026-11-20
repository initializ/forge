package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-cli/tools/computer"
	"github.com/initializ/forge/forge-core/types"
)

func TestComputerUseBackend_Gating(t *testing.T) {
	// Desktop profile requires the allow_native_host acknowledgment (decision 3).
	if _, ok, reason := computerUseBackend(types.ComputerUseConfig{Profile: "desktop"}); ok {
		t.Error("desktop profile without allow_native_host must be refused")
	} else if reason == "" {
		t.Error("refusal must carry a reason")
	}

	// Browser profile is not implemented in Phase 1.
	if _, ok, _ := computerUseBackend(types.ComputerUseConfig{Profile: "browser", AllowNativeHost: true}); ok {
		t.Error("browser profile must be refused in Phase 1")
	}

	// Unknown profile is refused.
	if _, ok, _ := computerUseBackend(types.ComputerUseConfig{Profile: "nope", AllowNativeHost: true}); ok {
		t.Error("unknown profile must be refused")
	}

	// Desktop + ack: succeeds on a supported OS (macOS), refused with a clear
	// macOS-only reason elsewhere — so the test is valid on any CI host.
	cfg := types.ComputerUseConfig{Profile: "desktop", AllowNativeHost: true}
	backend, ok, reason := computerUseBackend(cfg)
	if computer.DesktopSupported() {
		if !ok || backend == nil {
			t.Fatalf("desktop+ack should succeed on a supported OS, got reason=%q", reason)
		}
	} else if ok {
		t.Error("desktop must be refused on a non-macOS host in Phase 1")
	}

	// Empty profile defaults to desktop.
	if _, _, reason := computerUseBackend(types.ComputerUseConfig{AllowNativeHost: true}); computer.DesktopSupported() && reason != "" {
		t.Errorf("empty profile should default to desktop and succeed, got %q", reason)
	}
}
