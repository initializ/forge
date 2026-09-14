package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// Gateway model-credential resolver (#455 slice 1).
//
// An enterprise fronts the model with a gateway (Kong/Bedrock) behind an IdP
// (Okta/Entra) and issues a short-lived JWT. Following Claude Code's
// apiKeyHelper contract, an external command prints a token to stdout; we cache
// it (keyed by the token's JWT exp) and inject it as the model credential.
//
// This is a LOCAL-DEV affordance: the developer's checked-in forge.yaml stays
// native (provider + api key) for the server, while managed/user settings
// auto-wire the gateway locally (see the runtime overlay in runner.go).
//
// Tokens are cached keyed by the HELPER COMMAND (store key
// "gateway-<hash(helper)>") — the helper is the token's true identity (its IdP
// and audience), so two providers sharing a helper share one token, while
// distinct helpers get distinct tokens. This lets the login gate (which does
// not yet know the resolved provider) and the client-build overlay (which does)
// derive the SAME key, and works for both provider-scoped and catch-all
// gateways. Storage rides forge-core/llm/oauth: encrypted when FORGE_PASSPHRASE
// is set, else a 0600 file under ~/.forge/credentials.

// gatewayHelperTimeout bounds a single helper run. It is generous because the
// helper commonly opens a browser for an interactive IdP login (the reference
// Okta helper waits up to 120s for the OAuth callback).
const gatewayHelperTimeout = 3 * time.Minute

// gatewayRefreshBuffer re-runs the helper this long before the cached token's
// real expiry, so an in-flight request never rides a token about to expire.
const gatewayRefreshBuffer = 5 * time.Minute

// gatewayCredKey is the oauth-store key for a helper's gateway token, derived
// from the helper command so gate and overlay agree. A filesystem-safe form
// ("gateway-<hex>", no shell/path chars) keeps the plaintext fallback file name
// valid on every OS.
func gatewayCredKey(helperCmd string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(helperCmd)))
	return "gateway-" + hex.EncodeToString(sum[:])[:16]
}

// CachedGatewayToken returns the stored token for a helper WITHOUT running it.
// Returns (nil, nil) when none is cached. Used for token injection at
// client-build time and for `forge auth status`.
func CachedGatewayToken(helperCmd string) (*oauth.Token, error) {
	return oauth.LoadCredentials(gatewayCredKey(helperCmd))
}

// ClearGatewayToken deletes a helper's cached token (`forge auth logout`).
func ClearGatewayToken(helperCmd string) error {
	return oauth.DeleteCredentials(gatewayCredKey(helperCmd))
}

// EnsureGatewayToken returns a valid token for the helper, running it to
// (re)acquire one only when the cache is missing or within the refresh buffer
// of expiry. This is "login". The helper's stdout is the raw token; its expiry
// is read from the token's JWT exp claim (opaque/non-JWT tokens are cached with
// a zero expiry, i.e. re-fetched every call).
func EnsureGatewayToken(ctx context.Context, helperCmd string, env map[string]string) (*oauth.Token, error) {
	if strings.TrimSpace(helperCmd) == "" {
		return nil, fmt.Errorf("no api_key_helper configured")
	}

	key := gatewayCredKey(helperCmd)
	if tok, err := oauth.LoadCredentials(key); err == nil && tok != nil && tok.AccessToken != "" {
		if !tok.IsExpiredWithBuffer(gatewayRefreshBuffer) {
			return tok, nil
		}
	}

	raw, err := runGatewayHelper(ctx, helperCmd, env)
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("api_key_helper produced no token")
	}

	tok := &oauth.Token{AccessToken: raw, TokenType: "Bearer"}
	if exp, ok := oauth.ParseJWTExp(raw); ok {
		tok.ExpiresAt = exp
	}
	if err := oauth.SaveCredentials(key, tok); err != nil {
		return nil, fmt.Errorf("caching gateway token: %w", err)
	}
	return tok, nil
}

// runGatewayHelper executes the helper command and returns its stdout. The
// command is tokenized with a quote-aware splitter and run via
// exec.CommandContext WITHOUT a shell — no pipes, redirects, or globbing. This
// is safe because the helper string comes from managed/user SETTINGS (trusted
// operator/developer config), never from agent, LLM, or remote input. Helpers
// needing shell features wrap them in a script (as the reference Okta helper
// does) and configure that script's path here.
func runGatewayHelper(ctx context.Context, helperCmd string, env map[string]string) (string, error) {
	fields, err := splitCommand(helperCmd)
	if err != nil {
		return "", fmt.Errorf("parsing api_key_helper command: %w", err)
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("empty api_key_helper command")
	}

	cmdCtx, cancel := context.WithTimeout(ctx, gatewayHelperTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, fields[0], fields[1:]...) //nolint:gosec // command is trusted settings config, not agent input
	// The helper's config (e.g. OKTA_CLIENT_ID / OKTA_ISSUER) comes from the
	// gateway's settings `env`, injected on top of forge's own environment.
	if len(env) > 0 {
		merged := os.Environ()
		for k, v := range env {
			merged = append(merged, k+"="+v)
		}
		cmd.Env = merged
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("api_key_helper failed: %s", strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("api_key_helper failed: %w", err)
	}
	return stdout.String(), nil
}

// splitCommand tokenizes a command line into argv, honoring single and double
// quotes (no variable/command substitution — quotes only group tokens). It
// deliberately does NOT interpret shell metacharacters; those must live inside
// a wrapper script. An unterminated quote is an error.
func splitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	var quote rune // 0, '\'' or '"'

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}
