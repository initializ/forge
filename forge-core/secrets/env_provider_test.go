package secrets

import "testing"

func TestEnvProvider_GetSet(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "hunter2")

	p := NewEnvProvider("")
	if p.Name() != "env" {
		t.Fatalf("expected name 'env', got %q", p.Name())
	}

	val, err := p.Get("TEST_SECRET_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "hunter2" {
		t.Fatalf("expected 'hunter2', got %q", val)
	}
}

func TestEnvProvider_NotFound(t *testing.T) {
	p := NewEnvProvider("")
	_, err := p.Get("DEFINITELY_NOT_SET_EVER_12345")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected ErrSecretNotFound, got %T: %v", err, err)
	}
}

func TestEnvProvider_Prefix(t *testing.T) {
	t.Setenv("FORGE_MY_KEY", "prefixed-value")

	p := NewEnvProvider("FORGE_")
	val, err := p.Get("MY_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "prefixed-value" {
		t.Fatalf("expected 'prefixed-value', got %q", val)
	}
}

func TestEnvProvider_List(t *testing.T) {
	p := NewEnvProvider("")
	keys, err := p.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}
}
