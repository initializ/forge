package oauth

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// TestBuildAuthURL_MultipleParamsAreIntact pins the invariant that
// the built authorize URL carries every required OAuth 2.0 param
// (client_id, redirect_uri, scope, state, code_challenge,
// code_challenge_method) plus any provider-declared extras. The
// value is that a Windows regression where the URL was truncated at
// the first `&` (see openBrowser docs) presents to the user as a
// generic OpenAI "authentication error" with no obvious server-side
// pointer. Pinning the URL shape here catches URL-builder changes
// that would strip params; pairing with the openBrowser fix protects
// the launcher path.
func TestBuildAuthURL_MultipleParamsAreIntact(t *testing.T) {
	cfg := OpenAIConfig()
	f := NewFlow(cfg)
	authURL := f.buildAuthURL(&PKCEParams{
		Verifier:  "verifier-fixture",
		Challenge: "challenge-fixture",
		Method:    "S256",
	}, "state-fixture")

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	if u.Scheme+"://"+u.Host+u.Path != cfg.AuthURL {
		t.Errorf("scheme/host/path mismatch: got %q, want %q",
			u.Scheme+"://"+u.Host+u.Path, cfg.AuthURL)
	}
	q := u.Query()
	// Required OAuth 2.0 + PKCE fields.
	for _, key := range []string{
		"response_type", "client_id", "redirect_uri",
		"scope", "state", "code_challenge", "code_challenge_method",
	} {
		if q.Get(key) == "" {
			t.Errorf("required OAuth param %q missing from authorize URL", key)
		}
	}
	// The provider's extra params (OpenAI's Codex flow flags) must
	// also be present; losing them silently switches OpenAI to a
	// different consent variant.
	for k, v := range cfg.ExtraParams {
		if got := q.Get(k); got != v {
			t.Errorf("extra param %q: got %q, want %q", k, got, v)
		}
	}
	// The URL must contain at least seven `&` separators — the
	// count OpenAI needs to render the consent screen. If it drops
	// to zero (as it does when a Windows launcher's shell truncates
	// at the first `&`), the auth server returns "unknown_error".
	if amps := strings.Count(authURL, "&"); amps < 7 {
		t.Errorf("expected ≥7 `&` separators (multi-param URL); got %d — URL: %s",
			amps, authURL)
	}
}

// TestBrowserCommand pins the platform→launcher selection — the exact
// line the Windows fix changes, which no URL-shape test can see (a
// shell truncation is invisible until the browser opens). It asserts
// each GOOS builds the expected argv AND that the multi-`&` URL rides
// as a single, un-split argument — the invariant `cmd /c start`
// violated on Windows by letting cmd's parser eat everything after the
// first `&`.
func TestBrowserCommand(t *testing.T) {
	const multiParam = "https://auth.openai.com/authorize?response_type=code&client_id=x&scope=a+b&state=s"
	cases := []struct {
		goos string
		args []string // full argv incl. arg0; nil = unsupported → nil cmd
	}{
		{"darwin", []string{"open", multiParam}},
		{"linux", []string{"xdg-open", multiParam}},
		{"windows", []string{"rundll32", "url.dll,FileProtocolHandler", multiParam}},
		{"plan9", nil},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			cmd := browserCommand(tc.goos, multiParam)
			if tc.args == nil {
				if cmd != nil {
					t.Fatalf("unsupported %s must yield nil, got %v", tc.goos, cmd.Args)
				}
				return
			}
			if cmd == nil {
				t.Fatalf("%s yielded nil command", tc.goos)
			}
			if !reflect.DeepEqual(cmd.Args, tc.args) {
				t.Fatalf("%s argv = %v, want %v", tc.goos, cmd.Args, tc.args)
			}
			// The URL must survive as exactly one trailing argument —
			// no shell, no splitting on `&`.
			if last := cmd.Args[len(cmd.Args)-1]; last != multiParam {
				t.Errorf("%s: URL arg mutated/split: got %q, want %q", tc.goos, last, multiParam)
			}
		})
	}
}

// TestOpenAIConfig_ClientIDAndScopes pins the exact values Forge
// registers with OpenAI's OAuth. Rotating the ClientID or dropping
// `offline_access` from the scopes is a silent behavior change —
// tokens stop refreshing, sessions die after ~1h, and the failure
// mode is subtle. Test guards both.
func TestOpenAIConfig_ClientIDAndScopes(t *testing.T) {
	c := OpenAIConfig()
	if c.ClientID == "" {
		t.Fatal("ClientID must be set")
	}
	if !strings.Contains(c.Scopes, "offline_access") {
		t.Error("Scopes should include `offline_access` for refresh-token support")
	}
	if !strings.HasPrefix(c.AuthURL, "https://") || !strings.HasPrefix(c.TokenURL, "https://") {
		t.Error("Auth/Token URLs must be https")
	}
	if c.RedirectURI == "" || !strings.Contains(c.RedirectURI, "1455") {
		t.Errorf("RedirectURI should bind to the callback server's port 1455; got %q", c.RedirectURI)
	}
}
