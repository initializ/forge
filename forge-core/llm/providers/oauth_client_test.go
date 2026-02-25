package providers

import (
	"os"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/oauth"
)

func TestOAuthClient_ModelID(t *testing.T) {
	cfg := llm.ClientConfig{
		APIKey: "test-token",
		Model:  "gpt-4o",
	}
	client := NewOAuthClient(cfg, "openai", oauth.OpenAIConfig())
	if client.ModelID() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", client.ModelID())
	}
}

func TestOAuthClient_EnsureValidToken_NoCredentials(t *testing.T) {
	// Use a temp directory with no credentials
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	cfg := llm.ClientConfig{
		APIKey: "test-token",
		Model:  "gpt-4o",
	}
	client := NewOAuthClient(cfg, "testprovider", oauth.OpenAIConfig())

	err := client.ensureValidToken()
	if err == nil {
		t.Error("expected error when no credentials exist")
	}
}

func TestOAuthClient_EnsureValidToken_ValidToken(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// Store a valid token
	token := &oauth.Token{
		AccessToken:  "valid-access-token",
		RefreshToken: "valid-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	if err := oauth.SaveCredentials("testprovider2", token); err != nil {
		t.Fatalf("failed to save credentials: %v", err)
	}

	cfg := llm.ClientConfig{
		APIKey: "old-token",
		Model:  "gpt-4o",
	}
	client := NewOAuthClient(cfg, "testprovider2", oauth.OpenAIConfig())

	err := client.ensureValidToken()
	if err != nil {
		t.Errorf("expected no error for valid token, got: %v", err)
	}
}
