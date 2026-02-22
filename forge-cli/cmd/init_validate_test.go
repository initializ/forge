package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateProviderKey_OpenAI_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data": []}`))
	}))
	defer server.Close()

	orig := openaiValidationURL
	openaiValidationURL = server.URL
	defer func() { openaiValidationURL = orig }()

	err := validateProviderKey("openai", "valid-key")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestValidateProviderKey_OpenAI_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	orig := openaiValidationURL
	openaiValidationURL = server.URL
	defer func() { openaiValidationURL = orig }()

	err := validateProviderKey("openai", "bad-key")
	if err == nil {
		t.Fatal("expected error for unauthorized key")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error containing 'invalid', got: %v", err)
	}
}

func TestValidateProviderKey_Anthropic_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "valid-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "msg_test"}`))
	}))
	defer server.Close()

	orig := anthropicValidationURL
	anthropicValidationURL = server.URL
	defer func() { anthropicValidationURL = orig }()

	err := validateProviderKey("anthropic", "valid-key")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestValidateProviderKey_Anthropic_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	orig := anthropicValidationURL
	anthropicValidationURL = server.URL
	defer func() { anthropicValidationURL = orig }()

	err := validateProviderKey("anthropic", "bad-key")
	if err == nil {
		t.Fatal("expected error for unauthorized key")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error containing 'invalid', got: %v", err)
	}
}

func TestValidateProviderKey_Ollama_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"models": []}`))
	}))
	defer server.Close()

	orig := ollamaValidationURL
	ollamaValidationURL = server.URL
	defer func() { ollamaValidationURL = orig }()

	err := validateProviderKey("ollama", "")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestValidateProviderKey_Custom_AlwaysSucceeds(t *testing.T) {
	err := validateProviderKey("custom", "any-key")
	if err != nil {
		t.Fatalf("expected nil error for custom provider, got: %v", err)
	}
}

func TestValidateProviderKey_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	orig := openaiValidationURL
	openaiValidationURL = server.URL
	defer func() { openaiValidationURL = orig }()

	err := validateProviderKey("openai", "test-key")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestValidatePerplexityKey_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices": [{"message": {"content": "pong"}}]}`))
	}))
	defer server.Close()

	orig := perplexityValidationURL
	perplexityValidationURL = server.URL
	defer func() { perplexityValidationURL = orig }()

	err := validatePerplexityKey("valid-key")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestValidatePerplexityKey_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	orig := perplexityValidationURL
	perplexityValidationURL = server.URL
	defer func() { perplexityValidationURL = orig }()

	err := validatePerplexityKey("bad-key")
	if err == nil {
		t.Fatal("expected error for unauthorized key")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error containing 'invalid', got: %v", err)
	}
}
