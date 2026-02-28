package secrets

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrSecretNotFound(t *testing.T) {
	err := &ErrSecretNotFound{Key: "MY_KEY", Provider: "test"}

	if err.Error() != `secret "MY_KEY" not found in provider "test"` {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestIsNotFound(t *testing.T) {
	notFound := &ErrSecretNotFound{Key: "KEY", Provider: "p"}
	if !IsNotFound(notFound) {
		t.Fatal("expected IsNotFound to return true for ErrSecretNotFound")
	}

	wrapped := fmt.Errorf("wrap: %w", notFound)
	if !IsNotFound(wrapped) {
		t.Fatal("expected IsNotFound to return true for wrapped ErrSecretNotFound")
	}

	other := errors.New("some other error")
	if IsNotFound(other) {
		t.Fatal("expected IsNotFound to return false for non-ErrSecretNotFound")
	}

	if IsNotFound(nil) {
		t.Fatal("expected IsNotFound to return false for nil")
	}
}
