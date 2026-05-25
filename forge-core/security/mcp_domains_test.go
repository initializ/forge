package security

import (
	"reflect"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func TestMCPDomains_Empty(t *testing.T) {
	t.Parallel()
	if got := MCPDomains(types.MCPConfig{}); got != nil {
		t.Errorf("empty config: got %v, want nil", got)
	}
}

func TestMCPDomains_DeduplicatesAndSorts(t *testing.T) {
	t.Parallel()
	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "linear", URL: "https://mcp.linear.app/sse"},
		{Name: "internal", URL: "http://internal-mcp.svc.cluster.local:8080/mcp"},
		{Name: "shared", URL: "https://mcp.linear.app/other"}, // dedupe with first
	}}
	got := MCPDomains(cfg)
	want := []string{
		"internal-mcp.svc.cluster.local",
		"mcp.linear.app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMCPDomains_OAuthEndpoints_Included(t *testing.T) {
	t.Parallel()
	cfg := types.MCPConfig{Servers: []types.MCPServer{{
		Name: "linear", URL: "https://mcp.linear.app/sse",
		Auth: &types.MCPAuth{
			Type:         "oauth",
			ClientID:     "x",
			AuthorizeURL: "https://linear.app/oauth/authorize",
			TokenURL:     "https://api.linear.app/oauth/token",
		},
	}}}
	got := MCPDomains(cfg)
	want := []string{
		"api.linear.app",
		"linear.app",
		"mcp.linear.app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMCPDomains_MalformedURL_Skipped(t *testing.T) {
	t.Parallel()
	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "good", URL: "https://example.com/mcp"},
		{Name: "malformed", URL: "::not a url"},
	}}
	got := MCPDomains(cfg)
	want := []string{"example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMCPDomainSources_Tagged(t *testing.T) {
	t.Parallel()
	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "z-svc", URL: "https://z.example.com/mcp"},
		{Name: "a-svc", URL: "https://a.example.com/mcp"},
	}}
	got := MCPDomainSources(cfg)
	want := map[string]string{
		"a.example.com": "mcp:a-svc",
		"z.example.com": "mcp:z-svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMCPDomainSources_DeterministicFirstAcrossServers(t *testing.T) {
	t.Parallel()
	// Two servers share the same OAuth authorize host — should be
	// tagged with the lexicographically first server name.
	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "z-svc", URL: "https://z.example.com/mcp", Auth: &types.MCPAuth{
			Type: "oauth", AuthorizeURL: "https://shared.example.com/auth", TokenURL: "https://shared.example.com/token",
		}},
		{Name: "a-svc", URL: "https://a.example.com/mcp", Auth: &types.MCPAuth{
			Type: "oauth", AuthorizeURL: "https://shared.example.com/auth", TokenURL: "https://shared.example.com/token",
		}},
	}}
	got := MCPDomainSources(cfg)
	if got["shared.example.com"] != "mcp:a-svc" {
		t.Errorf("shared host tag = %q, want mcp:a-svc", got["shared.example.com"])
	}
}
