package security

import (
	"reflect"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func TestAPIDomains_Empty(t *testing.T) {
	t.Parallel()
	if got := APIDomains(types.APIConfig{}); got != nil {
		t.Errorf("empty config: got %v, want nil", got)
	}
}

func TestAPIDomains_DeduplicatesAndSorts(t *testing.T) {
	t.Parallel()
	cfg := types.APIConfig{Servers: []types.APIServer{
		{Name: "member", BaseURL: "https://member-service.test.initializ.ai"},
		{Name: "internal", BaseURL: "http://member-service.initializ-test.svc.cluster.local:8090"},
		{Name: "shared", BaseURL: "https://member-service.test.initializ.ai/v2"}, // dedupe with first
	}}
	got := APIDomains(cfg)
	want := []string{
		"member-service.initializ-test.svc.cluster.local",
		"member-service.test.initializ.ai",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestAPIDomains_MalformedURL_Skipped(t *testing.T) {
	t.Parallel()
	cfg := types.APIConfig{Servers: []types.APIServer{
		{Name: "good", BaseURL: "https://example.com"},
		{Name: "malformed", BaseURL: "::not a url"},
	}}
	got := APIDomains(cfg)
	want := []string{"example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestAPIDomainSources_Tagged(t *testing.T) {
	t.Parallel()
	cfg := types.APIConfig{Servers: []types.APIServer{
		{Name: "z-svc", BaseURL: "https://z.example.com"},
		{Name: "a-svc", BaseURL: "https://a.example.com"},
	}}
	got := APIDomainSources(cfg)
	want := map[string]string{
		"a.example.com": "api:a-svc",
		"z.example.com": "api:z-svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestAPIDomainSources_DeterministicFirstAcrossServers(t *testing.T) {
	t.Parallel()
	// Two servers share the same base_url host — the tag is the
	// lexicographically first server name (mirrors MCPDomainSources).
	cfg := types.APIConfig{Servers: []types.APIServer{
		{Name: "z-svc", BaseURL: "https://shared.example.com/z"},
		{Name: "a-svc", BaseURL: "https://shared.example.com/a"},
	}}
	got := APIDomainSources(cfg)
	if got["shared.example.com"] != "api:a-svc" {
		t.Errorf("shared host tag = %q, want api:a-svc", got["shared.example.com"])
	}
}
