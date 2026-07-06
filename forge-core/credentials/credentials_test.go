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

// captureSink is a testing AuditSink that records every emit for later
// inspection. Used by the revoked-semantics regression tests.
type captureSink struct {
	events []capturedEvent
}

type capturedEvent struct {
	name   string
	fields map[string]any
}

func (c *captureSink) Emit(_ context.Context, name string, fields map[string]any) {
	c.events = append(c.events, capturedEvent{name, fields})
}

// TestHandleClose_SelfExpiringWhenNoRevoke pins the audit-honesty fix
// reviewer @initializ-mk asked for on #236. Providers like STS and
// static don't have a Revoke API — the credential remains live at
// the source until its TTL expires. credential_revoked must be
// emitted with revoked=false + self_expiring=true so operators can
// distinguish "actually invalidated" from "tool finished, token
// still live."
func TestHandleClose_SelfExpiringWhenNoRevoke(t *testing.T) {
	sink := &captureSink{}
	h := &Handle{
		mat:       Materialization{Env: map[string]string{"K": "v"}}, // Revoke == nil
		kind:      "static",
		tool:      "cli_execute",
		binary:    "env",
		audit:     sink,
		revocable: false,
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	e := sink.events[0]
	if e.name != "credential_revoked" {
		t.Errorf("event: got %q want credential_revoked", e.name)
	}
	if e.fields["revoked"] != false {
		t.Errorf("revoked: got %v want false", e.fields["revoked"])
	}
	if e.fields["self_expiring"] != true {
		t.Errorf("self_expiring: got %v want true", e.fields["self_expiring"])
	}
}

// TestHandleClose_HardRevokeWhenProviderRevokes covers the other
// half: providers with a Revoke callback report revoked=true +
// self_expiring=false when Revoke succeeds.
func TestHandleClose_HardRevokeWhenProviderRevokes(t *testing.T) {
	sink := &captureSink{}
	var revokeCalled bool
	h := &Handle{
		mat: Materialization{
			Env: map[string]string{"K": "v"},
			Revoke: func(context.Context) error {
				revokeCalled = true
				return nil
			},
		},
		kind:      "vault_dynamic",
		tool:      "cli_execute",
		binary:    "psql",
		audit:     sink,
		revocable: true,
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !revokeCalled {
		t.Error("Revoke callback was not invoked")
	}
	e := sink.events[0]
	if e.fields["revoked"] != true || e.fields["self_expiring"] != false {
		t.Errorf("expected revoked=true self_expiring=false, got fields=%v", e.fields)
	}
}
