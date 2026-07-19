package validate

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func userServer(a *types.MCPAuth) types.ForgeConfig {
	return types.ForgeConfig{
		MCP: types.MCPConfig{Servers: []types.MCPServer{{Name: "atl", Auth: a}}},
	}
}

func TestValidateStandaloneDelegatedConsent(t *testing.T) {
	fullStandalone := &types.MCPAuth{
		Type: "user", ClientID: "c", AuthorizeURL: "https://idp/az", TokenURL: "https://idp/tok",
	}

	tests := []struct {
		name    string
		cfg     types.ForgeConfig
		wantErr string // substring; "" means no error expected
	}{
		{
			name:    "valid standalone",
			cfg:     userServer(fullStandalone),
			wantErr: "",
		},
		{
			name:    "missing endpoints",
			cfg:     userServer(&types.MCPAuth{Type: "user", ClientID: "c"}),
			wantErr: "explicit auth.authorize_url",
		},
		{
			name:    "client_credentials grant rejected",
			cfg:     userServer(&types.MCPAuth{Type: "user", ClientID: "c", AuthorizeURL: "https://idp/az", TokenURL: "https://idp/tok", Grant: "client_credentials"}),
			wantErr: "authorization_code grant",
		},
		{
			name: "managed (platform set) skips standalone checks",
			cfg: func() types.ForgeConfig {
				c := userServer(&types.MCPAuth{Type: "user", Ref: "mcp.atl"}) // no endpoints, but managed
				c.Platform = &types.PlatformConfig{TokenEndpoint: "https://platform/token"}
				return c
			}(),
			wantErr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &ValidationResult{}
			validateStandaloneDelegatedConsent(&tc.cfg, r)
			joined := strings.Join(r.Errors, "\n")
			if tc.wantErr == "" && len(r.Errors) != 0 {
				t.Fatalf("expected no errors, got: %s", joined)
			}
			if tc.wantErr != "" && !strings.Contains(joined, tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %s", tc.wantErr, joined)
			}
		})
	}
}
