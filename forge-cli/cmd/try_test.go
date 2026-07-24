package cmd

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-cli/config"
)

// TestQuickstartPreset_NoPlaceholderCredential pins that the preset scaffold
// writes NO provider-key placeholder. A placeholder in .env would be loaded by
// NewLocalSession, shadow the real runtime credential (OAuth / env / paste),
// and get sent to the provider as an invalid key.
func TestQuickstartPreset_NoPlaceholderCredential(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "quickstart")
	opts := quickstartPreset("openai", "gpt-5.4")
	opts.OutputDir = dir
	opts.Force = true
	if err := scaffold(opts); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(env), "your-api-key-here") {
		t.Errorf(".env carries a placeholder credential that would shadow runtime resolution:\n%s", env)
	}
	if strings.Contains(string(env), "OPENAI_API_KEY") {
		t.Errorf(".env must not scaffold OPENAI_API_KEY for the preset:\n%s", env)
	}
}

// TestQuickstartPreset_ScaffoldsValidConfig runs the demo preset through the
// real scaffold path and asserts the generated forge.yaml is valid and keyless.
func TestQuickstartPreset_ScaffoldsValidConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "quickstart")
	opts := quickstartPreset("ollama", "llama3.2")
	opts.OutputDir = dir
	opts.Force = true

	if err := scaffold(opts); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	cfg, err := config.LoadForgeConfig(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("LoadForgeConfig: %v", err)
	}
	if cfg.AgentID != "quickstart" {
		t.Errorf("agent_id = %q, want quickstart", cfg.AgentID)
	}
	if cfg.Model.Provider != "ollama" || cfg.Model.Name != "llama3.2" {
		t.Errorf("model = %q/%q, want ollama/llama3.2", cfg.Model.Provider, cfg.Model.Name)
	}
	if len(cfg.BuiltinTools) != 3 {
		t.Errorf("builtin_tools = %v, want 3 (http_request, datetime_now, math_calculate)", cfg.BuiltinTools)
	}
	// Egress is allowlist and keyless (wttr.in), never the old key-gated hosts.
	if cfg.Egress.Mode != "allowlist" {
		t.Errorf("egress mode = %q, want allowlist", cfg.Egress.Mode)
	}
	var hasWttr bool
	for _, d := range cfg.Egress.AllowedDomains {
		if d == "wttr.in" {
			hasWttr = true
		}
		if d == "api.openweathermap.org" || d == "api.weatherapi.com" {
			t.Errorf("egress lists key-gated host %q; the demo must stay keyless", d)
		}
	}
	if !hasWttr {
		t.Errorf("egress = %v, want it to include wttr.in", cfg.Egress.AllowedDomains)
	}
}

func TestResolveTryProvider_FlagsWin(t *testing.T) {
	res, err := resolveTryProvider(context.Background(),
		tryFlags{provider: "anthropic", model: "claude-x"}, nil, io.Discard, false, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Provider != "anthropic" || res.Model != "claude-x" {
		t.Errorf("got %q/%q, want anthropic/claude-x", res.Provider, res.Model)
	}
}

func TestResolveTryProvider_EnvPrecedence(t *testing.T) {
	// Anthropic is preferred over OpenAI when both are set.
	t.Setenv("ANTHROPIC_API_KEY", "sk-a")
	t.Setenv("OPENAI_API_KEY", "sk-o")
	res, err := resolveTryProvider(context.Background(), tryFlags{}, nil, io.Discard, false, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic (preferred over openai)", res.Provider)
	}
}

func TestResolveTryProvider_Ollama(t *testing.T) {
	isolateCreds(t)
	// A live listener stands in for the Ollama daemon.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	t.Setenv("OLLAMA_HOST", ln.Addr().String())

	res, err := resolveTryProvider(context.Background(), tryFlags{}, nil, io.Discard, false, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Provider != "ollama" {
		t.Errorf("provider = %q, want ollama", res.Provider)
	}
}

func TestResolveTryProvider_NoCredsNonInteractive(t *testing.T) {
	isolateCreds(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1") // unreachable

	_, err := resolveTryProvider(context.Background(), tryFlags{}, nil, io.Discard, false, false)
	if err == nil {
		t.Fatal("want an error when no credential is available and no TTY")
	}
}

func TestTryWorkspace(t *testing.T) {
	t.Run("keep targets cwd, no cleanup", func(t *testing.T) {
		dir, cleanup, err := tryWorkspace(true)
		if err != nil {
			t.Fatalf("tryWorkspace: %v", err)
		}
		if dir != filepath.Join(".", "forge-quickstart") {
			t.Errorf("dir = %q, want ./forge-quickstart", dir)
		}
		if cleanup != nil {
			t.Error("keep should have no cleanup func")
		}
	})
	t.Run("ephemeral temp dir is removed", func(t *testing.T) {
		dir, cleanup, err := tryWorkspace(false)
		if err != nil {
			t.Fatalf("tryWorkspace: %v", err)
		}
		base := filepath.Dir(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("temp base should exist: %v", err)
		}
		cleanup()
		if _, err := os.Stat(base); !os.IsNotExist(err) {
			t.Errorf("cleanup should remove the temp base, stat err = %v", err)
		}
	})
}

// isolateCreds points HOME at a throwaway dir so a developer's real OpenAI
// OAuth token can't leak into the resolution-order tests.
func isolateCreds(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
}
