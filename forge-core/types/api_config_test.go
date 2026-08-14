package types

import (
	"strings"
	"testing"
)

func TestAPIConfigValidate(t *testing.T) {
	okOp := APIOp{Name: "reverse_fee", Method: "POST", Path: "/reverse-fee"}
	okServer := APIServer{Name: "member", BaseURL: "https://member.svc", Ops: []APIOp{okOp}}

	cases := []struct {
		name    string
		cfg     APIConfig
		wantErr string // substring; "" = expect valid
	}{
		{name: "empty is valid", cfg: APIConfig{}},
		{name: "well-formed ok", cfg: APIConfig{Servers: []APIServer{okServer}}},
		{
			name:    "missing server name",
			cfg:     APIConfig{Servers: []APIServer{{BaseURL: "https://x", Ops: []APIOp{okOp}}}},
			wantErr: "name is required",
		},
		{
			name:    "missing base_url",
			cfg:     APIConfig{Servers: []APIServer{{Name: "member", Ops: []APIOp{okOp}}}},
			wantErr: "base_url is required",
		},
		{
			name:    "no operations",
			cfg:     APIConfig{Servers: []APIServer{{Name: "member", BaseURL: "https://x"}}},
			wantErr: "at least one operation",
		},
		{
			name:    "duplicate server name",
			cfg:     APIConfig{Servers: []APIServer{okServer, okServer}},
			wantErr: "duplicate server name",
		},
		{
			name: "duplicate op name",
			cfg: APIConfig{Servers: []APIServer{{Name: "member", BaseURL: "https://x", Ops: []APIOp{
				okOp, {Name: "reverse_fee", Method: "GET", Path: "/other"},
			}}}},
			wantErr: "duplicate operation name",
		},
		{
			name: "op missing method",
			cfg: APIConfig{Servers: []APIServer{{Name: "member", BaseURL: "https://x", Ops: []APIOp{
				{Name: "reverse_fee", Path: "/reverse-fee"},
			}}}},
			wantErr: "method is required",
		},
		{
			name: "op missing path",
			cfg: APIConfig{Servers: []APIServer{{Name: "member", BaseURL: "https://x", Ops: []APIOp{
				{Name: "reverse_fee", Method: "POST"},
			}}}},
			wantErr: "path is required",
		},
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
