package runtime

import (
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func TestBuildRemoteSessionStore(t *testing.T) {
	cases := []struct {
		name       string
		cfgMode    string
		cfgURL     string
		envMode    string
		envURL     string
		envToken   string
		wantRemote bool
	}{
		{name: "default file backend", wantRemote: false},
		{name: "config remote, full", cfgMode: "remote", cfgURL: "http://svc", envToken: "tok", wantRemote: true},
		{name: "env overrides to remote", envMode: "remote", cfgURL: "http://svc", envToken: "tok", wantRemote: true},
		{name: "remote without url falls back", cfgMode: "remote", envToken: "tok", wantRemote: false},
		{name: "remote without token falls back", cfgMode: "remote", cfgURL: "http://svc", wantRemote: false},
		{name: "env url overrides config url", cfgMode: "remote", cfgURL: "http://old", envURL: "http://new", envToken: "tok", wantRemote: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvSessionStore, tc.envMode)
			t.Setenv(EnvSessionStoreURL, tc.envURL)
			t.Setenv(EnvPlatformToken, tc.envToken)
			t.Setenv(EnvOrgID, "org-1")

			got := buildRemoteSessionStore("agt-1", tc.cfgMode, tc.cfgURL, nil)
			if tc.wantRemote {
				if _, ok := got.(*coreruntime.RemoteSessionStore); !ok {
					t.Fatalf("expected *RemoteSessionStore, got %T", got)
				}
			} else if got != nil {
				t.Fatalf("expected nil (file backend), got %T", got)
			}
		})
	}
}
