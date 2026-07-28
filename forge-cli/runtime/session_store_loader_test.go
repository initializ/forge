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

// #372: a configured remote session store must be selected even when
// memPersistence is off — an agent whose forge.yaml left persistence off
// (common for CI/BYO images, where the platform injects
// FORGE_SESSION_STORE=remote) otherwise dropped all session history.
func TestSelectSessionStore_RemoteIgnoresPersistenceGate(t *testing.T) {
	t.Setenv(EnvSessionStore, sessionStoreRemote)
	t.Setenv(EnvSessionStoreURL, "http://agent-builder.svc:8080/api/v1/agent-sessions")
	t.Setenv(EnvPlatformToken, "tok-platform")

	// memPersistence=false must NOT stop the remote store from engaging.
	store, desc := selectSessionStore(false, "fundraise", "", "", t.TempDir(), nil)
	if store == nil {
		t.Fatal("remote store must engage independent of memPersistence (#372)")
	}
	if desc["backend"] != "remote" {
		t.Fatalf("backend = %v, want remote", desc["backend"])
	}
}

// Without a remote store configured, the local file store still respects the
// memPersistence gate.
func TestSelectSessionStore_FileGatedOnPersistence(t *testing.T) {
	t.Setenv(EnvSessionStore, "")
	t.Setenv(EnvSessionStoreURL, "")
	t.Setenv(EnvPlatformToken, "")

	if store, _ := selectSessionStore(false, "a", "", "", t.TempDir(), nil); store != nil {
		t.Fatal("file store must NOT build when memPersistence is off")
	}
	store, desc := selectSessionStore(true, "a", "", "", t.TempDir(), nil)
	if store == nil || desc["backend"] != "file" {
		t.Fatalf("file store must build when memPersistence is on: store=%v desc=%v", store, desc)
	}
}
