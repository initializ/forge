package types

import (
	"strings"
	"testing"
	"time"
)

func TestPdpConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     PdpConfig
		wantErr string // substring; "" = expect valid
	}{
		{name: "disabled is always valid", cfg: PdpConfig{Enabled: false, Fail: "open"}},
		{name: "enabled closed ok", cfg: PdpConfig{Enabled: true, Endpoint: "http://x/decide", Fail: "closed"}},
		{name: "enabled default fail ok", cfg: PdpConfig{Enabled: true, Endpoint: "http://x/decide", Timeout: 3 * time.Second}},
		{name: "enabled without endpoint", cfg: PdpConfig{Enabled: true, Fail: "closed"}, wantErr: "no endpoint"},
		{name: "fail open rejected", cfg: PdpConfig{Enabled: true, Endpoint: "http://x", Fail: "open"}, wantErr: "must fail closed"},
		{name: "fail garbage rejected", cfg: PdpConfig{Enabled: true, Endpoint: "http://x", Fail: "maybe"}, wantErr: "must be 'closed'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
