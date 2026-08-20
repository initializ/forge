package types

import (
	"strings"
	"testing"
	"time"
)

// The PDP endpoint must be env-expanded AT LOAD so startup validation sees the
// resolved value — an unset ${PDP_ENDPOINT} must fail loud, not degrade to a
// per-call deny-all when the resolver later expands it to "".
func TestParseForgeConfig_PDPEndpointExpandedAtLoad(t *testing.T) {
	doc := "agent_id: a\nversion: v1\nentrypoint: main\n" +
		"security:\n  pdp:\n    enabled: true\n    endpoint: ${PDP_ENDPOINT}\n    fail: closed\n"

	t.Setenv("PDP_ENDPOINT", "") // unset/empty
	cfg, err := ParseForgeConfig([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Security.Pdp.Endpoint != "" {
		t.Errorf("endpoint = %q, want empty after expanding an unset var at load", cfg.Security.Pdp.Endpoint)
	}
	if cfg.Security.Pdp.Validate() == nil {
		t.Error("Validate must fail loud on the expanded-empty endpoint, not pass and deny-all at runtime")
	}

	t.Setenv("PDP_ENDPOINT", "http://security-next/security/v1/pdp/decide")
	cfg2, err := ParseForgeConfig([]byte(doc))
	if err != nil {
		t.Fatalf("parse (set): %v", err)
	}
	if cfg2.Security.Pdp.Endpoint != "http://security-next/security/v1/pdp/decide" {
		t.Errorf("endpoint = %q, want the resolved URL", cfg2.Security.Pdp.Endpoint)
	}
	if err := cfg2.Security.Pdp.Validate(); err != nil {
		t.Errorf("Validate should pass with a resolved endpoint: %v", err)
	}
}

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
