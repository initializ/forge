package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
)

func gwJWT(exp int64) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(`{"exp":`+strconv.FormatInt(exp, 10)+`}`) + "." + enc("s")
}

// setupGatewaySettings points the USER settings layer at a temp file declaring a
// single openai gateway with an api_key_helper, and isolates the credential
// store. Returns the helper command string.
func setupGatewaySettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oauth.SetCredentialsDir(filepath.Join(dir, "creds"))
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	token := gwJWT(time.Now().Add(time.Hour).Unix())
	helper := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '"+token+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(dir, "settings.json")
	body := fmt.Sprintf(`{"models":{"gateways":[{"provider":"openai","base_url":"https://gw","auth_scheme":"bearer","api_key_helper":%q}]}}`, helper)
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvUserSettings, settingsPath)
	return helper
}

func newCmd() (*cobra.Command, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	c := &cobra.Command{}
	c.SetOut(buf)
	c.SetContext(context.Background())
	return c, buf
}

func TestAuthGateway_LoginStatusLogoutRoundTrip(t *testing.T) {
	helper := setupGatewaySettings(t)

	// login openai → caches a token.
	c, buf := newCmd()
	if err := runAuthLogin(c, []string{"openai"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(buf.String(), "Logged in to gateway openai") {
		t.Errorf("login output = %q", buf.String())
	}
	if tok, _ := runtime.CachedGatewayToken(helper); tok == nil || tok.AccessToken == "" {
		t.Fatal("expected a cached token after login")
	}

	// status → reports the token as valid.
	c, buf = newCmd()
	if err := runAuthStatus(c, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(buf.String(), "openai:") || !strings.Contains(buf.String(), "valid until") {
		t.Errorf("status output = %q", buf.String())
	}

	// logout openai → clears the gateway token too.
	c, buf = newCmd()
	if err := runAuthLogout(c, []string{"openai"}); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(buf.String(), "Cleared gateway token for openai") {
		t.Errorf("logout output = %q", buf.String())
	}
	if tok, _ := runtime.CachedGatewayToken(helper); tok != nil && tok.AccessToken != "" {
		t.Error("gateway token should be cleared after logout")
	}
}

func TestAuthGateway_LoginNoHelperConfigured(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(filepath.Join(dir, "creds"))
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	// Empty user settings → no gateway helper.
	sp := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(sp, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvUserSettings, sp)

	c, _ := newCmd()
	if err := runAuthLogin(c, nil); err == nil {
		t.Error("expected an error when no api_key_helper is configured")
	}
}

func TestAuthGateway_StatusNoGateways(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(sp, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvUserSettings, sp)

	c, buf := newCmd()
	if err := runAuthStatus(c, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(buf.String(), "No api_key_helper gateways configured") {
		t.Errorf("status output = %q", buf.String())
	}
}
