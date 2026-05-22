package statictoken_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/auth/providers/statictoken"
)

func TestNew_ValidationErrors(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		_, err := statictoken.New(statictoken.Config{})
		if !errors.Is(err, auth.ErrProviderNotConfigured) {
			t.Fatalf("err = %v, want ErrProviderNotConfigured", err)
		}
	})

	t.Run("token_env points at unset variable", func(t *testing.T) {
		t.Setenv("FORGE_TEST_TOKEN_DOES_NOT_EXIST", "")
		_, err := statictoken.New(statictoken.Config{TokenEnv: "FORGE_TEST_TOKEN_DOES_NOT_EXIST"})
		if !errors.Is(err, auth.ErrProviderNotConfigured) {
			t.Fatalf("err = %v, want ErrProviderNotConfigured", err)
		}
	})
}

func TestVerify_MatchReturnsIdentity(t *testing.T) {
	p, err := statictoken.New(statictoken.Config{
		Token: "secret-token",
		Identity: auth.Identity{
			UserID: "internal",
			Email:  "system@forge",
			Source: "internal",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := p.Verify(context.Background(), "secret-token", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id == nil {
		t.Fatal("Verify returned nil identity")
	}
	if id.UserID != "internal" || id.Email != "system@forge" {
		t.Errorf("identity = %+v", id)
	}
	if id.Source != "internal" {
		t.Errorf("identity.Source = %q, want %q", id.Source, "internal")
	}
}

func TestVerify_MatchDefaultSource(t *testing.T) {
	p, _ := statictoken.New(statictoken.Config{Token: "secret-token"})
	id, err := p.Verify(context.Background(), "secret-token", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Source != "static_token" {
		t.Errorf("identity.Source = %q, want %q (default)", id.Source, "static_token")
	}
}

func TestVerify_MismatchYieldsToNext(t *testing.T) {
	p, _ := statictoken.New(statictoken.Config{Token: "expected"})

	_, err := p.Verify(context.Background(), "different", nil)
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Fatalf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestVerify_EmptyTokenYields(t *testing.T) {
	p, _ := statictoken.New(statictoken.Config{Token: "expected"})

	_, err := p.Verify(context.Background(), "", nil)
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Fatalf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestTokenEnv_PrecedenceOverLiteral(t *testing.T) {
	t.Setenv("FORGE_TEST_OVERRIDE_TOKEN", "from-env")

	p, err := statictoken.New(statictoken.Config{
		Token:    "from-literal",
		TokenEnv: "FORGE_TEST_OVERRIDE_TOKEN",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Env value should win.
	if _, err := p.Verify(context.Background(), "from-env", nil); err != nil {
		t.Errorf("env token rejected: %v", err)
	}
	if _, err := p.Verify(context.Background(), "from-literal", nil); !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("literal token accepted (should be overridden by env): %v", err)
	}
}

func TestTokenEnv_FallsBackToLiteralWhenEnvEmpty(t *testing.T) {
	t.Setenv("FORGE_TEST_UNSET_TOKEN", "")

	p, err := statictoken.New(statictoken.Config{
		Token:    "from-literal",
		TokenEnv: "FORGE_TEST_UNSET_TOKEN",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := p.Verify(context.Background(), "from-literal", nil); err != nil {
		t.Errorf("literal token rejected when env was empty: %v", err)
	}
}

func TestIdentityIsDefensivelyCopied(t *testing.T) {
	cfg := statictoken.Config{
		Token: "tok",
		Identity: auth.Identity{
			UserID: "u",
			Groups: []string{"a", "b"},
			Claims: map[string]any{"role": "admin"},
		},
	}
	p, _ := statictoken.New(cfg)

	id1, _ := p.Verify(context.Background(), "tok", nil)
	// Mutate the returned identity.
	id1.Groups[0] = "MUTATED"
	id1.Claims["role"] = "MUTATED"

	id2, _ := p.Verify(context.Background(), "tok", nil)
	if id2.Groups[0] != "a" {
		t.Errorf("Groups[0] = %q after mutation, want %q (defensive copy failed)", id2.Groups[0], "a")
	}
	if id2.Claims["role"] != "admin" {
		t.Errorf("Claims[role] = %v after mutation, want admin (defensive copy failed)", id2.Claims["role"])
	}
}

func TestConcurrentVerify_RaceSafe(t *testing.T) {
	// Race-safety smoke. Run with `go test -race`.
	p, _ := statictoken.New(statictoken.Config{Token: "tok"})

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			_, _ = p.Verify(context.Background(), "tok", nil)
			_, _ = p.Verify(context.Background(), "wrong", nil)
		})
	}
	wg.Wait()
}

func TestRegisteredViaFactory(t *testing.T) {
	p, err := auth.Build("static_token", map[string]any{
		"token": "abc",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "static_token" {
		t.Errorf("Name = %q, want static_token", p.Name())
	}
}

func TestFactory_UsesTokenEnv(t *testing.T) {
	t.Setenv("FORGE_TEST_FACTORY_TOKEN", "via-env")

	p, err := auth.Build("static_token", map[string]any{
		"token_env": "FORGE_TEST_FACTORY_TOKEN",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := p.Verify(context.Background(), "via-env", nil); err != nil {
		t.Errorf("env-resolved token rejected: %v", err)
	}
}
