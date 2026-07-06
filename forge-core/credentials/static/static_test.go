package static

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/credentials"
)

func TestStaticProvider_RegisteredAtInit(t *testing.T) {
	if p := credentials.DefaultRegistry.Get(ProviderName); p == nil {
		t.Fatal("static provider not registered by init()")
	}
}

func TestStaticProvider_MaterializeReturnsSpecEnv(t *testing.T) {
	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec: json.RawMessage(`{
			"env": {"AWS_ACCESS_KEY_ID": "AKIAFAKE", "AWS_SECRET_ACCESS_KEY": "abc"},
			"ttl": "1h"
		}`),
	}
	cred, err := Provider{}.NewCredential(context.Background(), spec)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	mat, err := cred.Materialize(context.Background(), "cli_execute", nil)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if mat.Env["AWS_ACCESS_KEY_ID"] != "AKIAFAKE" {
		t.Errorf("env not injected: %v", mat.Env)
	}
	if mat.TTL != "1h" {
		t.Errorf("ttl not surfaced: %q", mat.TTL)
	}
	if mat.Revoke != nil {
		t.Errorf("static provider must not set Revoke")
	}
}

func TestStaticProvider_ClonesEnvSoCallerCantMutateSpec(t *testing.T) {
	spec := credentials.CredentialSpec{
		Provider: ProviderName,
		Spec:     json.RawMessage(`{"env": {"K": "v"}}`),
	}
	cred, _ := Provider{}.NewCredential(context.Background(), spec)
	m1, _ := cred.Materialize(context.Background(), "", nil)
	m1.Env["K"] = "mutated"
	m2, _ := cred.Materialize(context.Background(), "", nil)
	if m2.Env["K"] != "v" {
		t.Errorf("provider leaked mutation across Materialize calls: got %q", m2.Env["K"])
	}
}

func TestStaticProvider_EmptySpec_ReturnsEmptyMaterialization(t *testing.T) {
	cred, err := Provider{}.NewCredential(context.Background(), credentials.CredentialSpec{Provider: ProviderName})
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	mat, err := cred.Materialize(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(mat.Env) != 0 || len(mat.Headers) != 0 {
		t.Errorf("expected empty materialization, got env=%v headers=%v", mat.Env, mat.Headers)
	}
}

func TestStaticProvider_InvalidJSONSpecErrors(t *testing.T) {
	_, err := Provider{}.NewCredential(context.Background(), credentials.CredentialSpec{
		Provider: ProviderName,
		Spec:     json.RawMessage(`{invalid`),
	})
	if err == nil {
		t.Fatal("expected error on malformed spec")
	}
}
