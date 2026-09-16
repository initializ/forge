package cmd

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
)

// TestGatewayGateApplies pins which commands the login gate covers: the
// LLM-touching ones (run, try, serve/serve start) and NOT management verbs
// (serve stop/status/logs, tool list, auth *, offline commands).
func TestGatewayGateApplies(t *testing.T) {
	root := &cobra.Command{Use: "forge"}

	run := &cobra.Command{Use: "run"}
	try := &cobra.Command{Use: "try"}
	validate := &cobra.Command{Use: "validate"}
	settings := &cobra.Command{Use: "settings"}

	serve := &cobra.Command{Use: "serve"}
	serveStart := &cobra.Command{Use: "start"}
	serveStop := &cobra.Command{Use: "stop"}
	serve.AddCommand(serveStart, serveStop)

	tool := &cobra.Command{Use: "tool"}
	toolList := &cobra.Command{Use: "list"}
	tool.AddCommand(toolList)

	auth := &cobra.Command{Use: "auth"}
	authLogin := &cobra.Command{Use: "login"}
	auth.AddCommand(authLogin)

	root.AddCommand(run, try, validate, settings, serve, tool, auth)

	cases := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"run", run, true},
		{"try", try, true},
		{"serve (bare)", serve, true},
		{"serve start", serveStart, true},
		{"serve stop", serveStop, false},
		{"tool list", toolList, false},
		{"auth login", authLogin, false},
		{"validate", validate, false},
		{"settings", settings, false},
		{"root", root, false},
	}
	for _, c := range cases {
		if got := gatewayGateApplies(c.cmd); got != c.want {
			t.Errorf("gatewayGateApplies(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func gateJWT(exp int64) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(`{"exp":`+strconv.FormatInt(exp, 10)+`}`) + "." + enc("s")
}

// TestGatewayLoginGate_CatchAllUsesMergedEnv pins the PR #464 LOW #4 fix: the
// gate must cache the catch-all gateway token under the SAME (helper, env) key
// the runtime overlay looks it up by — i.e. the trusted-MERGED env, not the
// managed-layer-only env. Managed arms the helper; a user-layer catch-all adds
// env; the gate must mint+cache under the merged env.
func TestGatewayLoginGate_CatchAllUsesMergedEnv(t *testing.T) {
	t.Chdir(t.TempDir()) // no project-layer .forge to interfere
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	dir := t.TempDir()
	token := gateJWT(time.Now().Add(time.Hour).Unix())
	helper := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '"+token+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Managed layer: a catch-all gateway with the helper + env A (this ARMS the gate).
	managedDir := t.TempDir()
	managed := `{"models":{"gateway":{"api_key_helper":"` + helper + `","env":{"A":"1"}}}}`
	if err := os.WriteFile(filepath.Join(managedDir, "managed-settings.json"), []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := settings.SetManagedDirForTest(managedDir)
	t.Cleanup(restore)

	// User layer: same catch-all gateway contributing env B (additive merge).
	userFile := filepath.Join(dir, "user.json")
	if err := os.WriteFile(userFile, []byte(`{"models":{"gateway":{"env":{"B":"2"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvUserSettings, userFile)

	// Build a real `forge run` command tree so the gate applies.
	root := &cobra.Command{Use: "forge"}
	run := &cobra.Command{Use: "run"}
	root.AddCommand(run)
	run.SetContext(context.Background())

	if err := gatewayLoginGate(run, nil); err != nil {
		t.Fatalf("gate: %v", err)
	}

	mergedEnv := map[string]string{"A": "1", "B": "2"}
	managedOnlyEnv := map[string]string{"A": "1"}
	// The overlay looks up the token under the MERGED env — it must be present.
	if tok, _ := runtime.CachedGatewayToken(helper, mergedEnv); tok == nil || tok.AccessToken != token {
		t.Errorf("token not cached under the merged-env key the overlay reads: %+v", tok)
	}
	// It must NOT be cached under the managed-only env key (the old bug).
	if tok, _ := runtime.CachedGatewayToken(helper, managedOnlyEnv); tok != nil && tok.AccessToken != "" {
		t.Error("token cached under managed-only env key — gate/overlay keys diverge")
	}
}
