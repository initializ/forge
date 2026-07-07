package types

import (
	"strings"
	"testing"
	"time"
)

// TestDeferConfig_Validate pins the fail-loud invariant Manoj asked
// for on #248: an operator who sets `enabled: true` without listing
// any tools gets a startup error, not a silent no-op with a
// misleading "defer engine wired" log line.
func TestDeferConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     DeferConfig
		wantErr bool
	}{
		{
			name:    "disabled_is_always_ok",
			cfg:     DeferConfig{Enabled: false},
			wantErr: false,
		},
		{
			name:    "disabled_with_tools_is_ok",
			cfg:     DeferConfig{Enabled: false, Tools: map[string]DeferToolConfig{"cli_execute": {}}},
			wantErr: false,
		},
		{
			name:    "enabled_without_tools_errors",
			cfg:     DeferConfig{Enabled: true},
			wantErr: true,
		},
		{
			name:    "enabled_with_nil_tools_map_errors",
			cfg:     DeferConfig{Enabled: true, Tools: nil},
			wantErr: true,
		},
		{
			name:    "enabled_with_empty_tools_map_errors",
			cfg:     DeferConfig{Enabled: true, Tools: map[string]DeferToolConfig{}},
			wantErr: true,
		},
		{
			name: "enabled_with_one_tool_is_ok",
			cfg: DeferConfig{
				Enabled: true,
				Tools:   map[string]DeferToolConfig{"cli_execute": {Timeout: 5 * time.Minute}},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if tc.wantErr && err != nil {
				// Message must name the block so the operator can find
				// the offending forge.yaml stanza fast.
				if !strings.Contains(err.Error(), "defer") {
					t.Errorf("error should name `defer`: %q", err)
				}
			}
		})
	}
}
