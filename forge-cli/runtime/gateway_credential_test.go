package runtime

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// writeHelper writes an executable shell script that prints out to stdout and,
// each time it runs, appends a byte to a marker file so a test can assert how
// many times the helper actually executed.
func writeHelper(t *testing.T, dir, name, out, marker string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := fmt.Sprintf("#!/bin/sh\nprintf 'x' >> %q\nprintf '%s'\n", marker, out)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return path
}

func jwtWithExp(exp int64) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(`{"exp":`+strconv.FormatInt(exp, 10)+`}`) + "." + enc("sig")
}

func markerRuns(t *testing.T, marker string) int {
	t.Helper()
	b, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return len(b)
}

func TestEnsureGatewayToken_FetchCacheReuse(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	marker := filepath.Join(dir, "runs")
	token := jwtWithExp(time.Now().Add(time.Hour).Unix())
	helper := writeHelper(t, dir, "helper.sh", token, marker)

	// First call runs the helper and caches.
	tok, err := EnsureGatewayToken(context.Background(), helper, nil)
	if err != nil {
		t.Fatalf("first EnsureGatewayToken: %v", err)
	}
	if tok.AccessToken != token {
		t.Fatalf("token mismatch")
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("expected ExpiresAt from JWT exp")
	}
	if runs := markerRuns(t, marker); runs != 1 {
		t.Fatalf("helper ran %d times, want 1", runs)
	}

	// Second call is a cache hit — helper must NOT run again.
	if _, err := EnsureGatewayToken(context.Background(), helper, nil); err != nil {
		t.Fatalf("second EnsureGatewayToken: %v", err)
	}
	if runs := markerRuns(t, marker); runs != 1 {
		t.Errorf("cache hit should not re-run helper; ran %d times", runs)
	}
}

func TestEnsureGatewayToken_ExpiredReRuns(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	marker := filepath.Join(dir, "runs")
	fresh := jwtWithExp(time.Now().Add(time.Hour).Unix())
	helper := writeHelper(t, dir, "helper.sh", fresh, marker)

	// Pre-seed an EXPIRED cached token under the helper's key.
	if err := oauth.SaveCredentials(GatewayCredKey(helper, nil), &oauth.Token{
		AccessToken: "stale", ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	tok, err := EnsureGatewayToken(context.Background(), helper, nil)
	if err != nil {
		t.Fatalf("EnsureGatewayToken: %v", err)
	}
	if tok.AccessToken != fresh {
		t.Errorf("expired token should be refreshed; got %q", tok.AccessToken)
	}
	if runs := markerRuns(t, marker); runs != 1 {
		t.Errorf("expired cache should re-run helper once, ran %d", runs)
	}
}

func TestEnsureGatewayToken_OpaqueTokenNotCachedByExpiry(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	marker := filepath.Join(dir, "runs")
	helper := writeHelper(t, dir, "helper.sh", "opaque-not-a-jwt", marker)

	tok, err := EnsureGatewayToken(context.Background(), helper, nil)
	if err != nil {
		t.Fatalf("EnsureGatewayToken: %v", err)
	}
	if !tok.ExpiresAt.IsZero() {
		t.Error("opaque token should have zero ExpiresAt")
	}
	// Zero expiry ⇒ always considered expired ⇒ the helper re-runs every call.
	if _, err := EnsureGatewayToken(context.Background(), helper, nil); err != nil {
		t.Fatal(err)
	}
	if runs := markerRuns(t, marker); runs != 2 {
		t.Errorf("opaque token should re-run helper each call; ran %d, want 2", runs)
	}
}

func TestEnsureGatewayToken_EmptyHelper(t *testing.T) {
	if _, err := EnsureGatewayToken(context.Background(), "   ", nil); err == nil {
		t.Error("expected error for empty helper command")
	}
}

// TestEnsureGatewayToken_InjectsEnv verifies the gateway's settings `env` is
// injected into the helper subprocess — so config like OKTA_CLIENT_ID can live
// in settings rather than requiring a shell export.
func TestEnsureGatewayToken_InjectsEnv(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	// The helper echoes the value of OKTA_CLIENT_ID it received as its token.
	helper := filepath.Join(dir, "envhelper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s' \"$OKTA_CLIENT_ID\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tok, err := EnsureGatewayToken(context.Background(), helper, map[string]string{"OKTA_CLIENT_ID": "client-123"})
	if err != nil {
		t.Fatalf("EnsureGatewayToken: %v", err)
	}
	if tok.AccessToken != "client-123" {
		t.Errorf("helper did not receive injected env: token = %q, want %q", tok.AccessToken, "client-123")
	}
}

func TestCachedGatewayToken_AbsentIsNil(t *testing.T) {
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	tok, err := CachedGatewayToken("some.sh", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if tok != nil {
		t.Errorf("expected nil token when none cached, got %+v", tok)
	}
}

func TestGatewayCredKey_HelperAndEnvScoped(t *testing.T) {
	// Same (helper, env) → same key (gate and overlay must agree); different
	// helper OR different env → different keys. env is part of the identity so a
	// shared helper parameterized per-provider doesn't collide.
	a1 := GatewayCredKey("a.sh", map[string]string{"ISSUER": "A"})
	a2 := GatewayCredKey("a.sh", map[string]string{"ISSUER": "A"})
	if a1 != a2 {
		t.Error("key must be stable for the same (helper, env)")
	}
	if a1 == GatewayCredKey("b.sh", map[string]string{"ISSUER": "A"}) {
		t.Error("distinct helpers must get distinct keys")
	}
	if a1 == GatewayCredKey("a.sh", map[string]string{"ISSUER": "B"}) {
		t.Error("same helper with different env must get distinct keys")
	}
	if GatewayCredKey("a.sh", nil) == GatewayCredKey("a.sh", map[string]string{"ISSUER": "A"}) {
		t.Error("no env vs some env must differ")
	}
}

// TestEnsureGatewayToken_SharedHelperDifferentEnv is the review MEDIUM #1/#2
// case: one helper reused across two gateways with different env must mint and
// cache DISTINCT tokens (env parameterizes the token's issuer/audience), not
// collide under a helper-only key.
func TestEnsureGatewayToken_SharedHelperDifferentEnv(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	// The helper echoes whatever ISSUER it received, so the two calls yield
	// different token strings.
	helper := filepath.Join(dir, "shared.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'token-%s' \"$ISSUER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	envA := map[string]string{"ISSUER": "A"}
	envB := map[string]string{"ISSUER": "B"}

	tokA, err := EnsureGatewayToken(context.Background(), helper, envA)
	if err != nil {
		t.Fatalf("EnsureGatewayToken A: %v", err)
	}
	tokB, err := EnsureGatewayToken(context.Background(), helper, envB)
	if err != nil {
		t.Fatalf("EnsureGatewayToken B: %v", err)
	}
	if tokA.AccessToken != "token-A" || tokB.AccessToken != "token-B" {
		t.Fatalf("tokens = %q / %q, want token-A / token-B", tokA.AccessToken, tokB.AccessToken)
	}
	// Each is cached under its own (helper, env) key — no collision.
	if cached, _ := CachedGatewayToken(helper, envA); cached == nil || cached.AccessToken != "token-A" {
		t.Errorf("envA cache = %+v, want token-A", cached)
	}
	if cached, _ := CachedGatewayToken(helper, envB); cached == nil || cached.AccessToken != "token-B" {
		t.Errorf("envB cache = %+v, want token-B", cached)
	}
}

func TestValidateHelperEnv(t *testing.T) {
	for _, bad := range []map[string]string{
		{"": "v"},         // empty key
		{"BAD-KEY": "v"},  // illegal char
		{"9LEADING": "v"}, // leading digit
		{"K": "a\x00b"},   // NUL in value
	} {
		if err := validateHelperEnv(bad); err == nil {
			t.Errorf("validateHelperEnv(%v): expected error", bad)
		}
	}
	if err := validateHelperEnv(map[string]string{"OKTA_CLIENT_ID": "x", "_A9": "y"}); err != nil {
		t.Errorf("valid env rejected: %v", err)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{`helper.sh`, []string{"helper.sh"}, false},
		{`  helper.sh  --flag  x `, []string{"helper.sh", "--flag", "x"}, false},
		{`sh -c "echo hi"`, []string{"sh", "-c", "echo hi"}, false},
		{`a 'b c' d`, []string{"a", "b c", "d"}, false},
		{`a "unterminated`, nil, true},
		{``, nil, false},
	}
	for _, c := range cases {
		got, err := splitCommand(c.in)
		if c.err {
			if err == nil {
				t.Errorf("splitCommand(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitCommand(%q): unexpected error %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("splitCommand(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCommand(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
