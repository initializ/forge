package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeProvider struct {
	name string
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) NewCredential(_ context.Context, cs CredentialSpec) (Credential, error) {
	return &fakeCred{kind: f.name, spec: cs}, nil
}

type fakeCred struct {
	kind string
	spec CredentialSpec
}

func (c *fakeCred) Kind() string { return c.kind }
func (c *fakeCred) Materialize(_ context.Context, _ string, _ json.RawMessage) (Materialization, error) {
	return Materialization{Env: map[string]string{"FROM": c.kind}}, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeProvider{name: "foo"})
	if p := r.Get("foo"); p == nil {
		t.Fatal("Get returned nil for registered provider")
	}
	if p := r.Get("missing"); p != nil {
		t.Errorf("Get returned non-nil for unregistered provider: %+v", p)
	}
}

func TestRegistry_DuplicateRegistrationPanics(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeProvider{name: "dup"})
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate provider name")
		}
	}()
	r.Register(fakeProvider{name: "dup"})
}

func TestRegistry_ResolveSpec_UnknownProviderErrors(t *testing.T) {
	r := NewRegistry()
	_, err := r.ResolveSpec(context.Background(), CredentialSpec{Provider: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("expected ErrUnknownProvider, got %v", err)
	}
}

func TestRegistry_ResolveSpec_HappyPath(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeProvider{name: "aws"})
	cred, err := r.ResolveSpec(context.Background(), CredentialSpec{Provider: "aws"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Kind() != "aws" {
		t.Errorf("Kind: got %q want aws", cred.Kind())
	}
	mat, err := cred.Materialize(context.Background(), "cli_execute", nil)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if mat.Env["FROM"] != "aws" {
		t.Errorf("Env: got %v", mat.Env)
	}
}

func TestCredentialSpec_MatchesTool(t *testing.T) {
	cases := []struct {
		name         string
		spec         CredentialSpec
		tool, binary string
		want         bool
	}{
		{"unscoped matches everything", CredentialSpec{}, "cli_execute", "aws", true},
		{"tool match", CredentialSpec{Tool: "cli_execute"}, "cli_execute", "aws", true},
		{"tool mismatch", CredentialSpec{Tool: "cli_execute"}, "http_request", "", false},
		{"binary match", CredentialSpec{Tool: "cli_execute", Binary: "aws"}, "cli_execute", "aws", true},
		{"binary mismatch", CredentialSpec{Tool: "cli_execute", Binary: "aws"}, "cli_execute", "gcloud", false},
		{"binary ignored on non-cli tool", CredentialSpec{Tool: "http_request", Binary: "aws"}, "http_request", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.spec.MatchesTool(c.tool, c.binary); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}
