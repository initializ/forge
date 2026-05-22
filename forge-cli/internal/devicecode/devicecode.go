// Package devicecode implements the OAuth 2.0 device-authorization grant
// (RFC 8628) against Microsoft Entra ID. The helpers here are shared between
// the standalone `forge channel msteams-login` CLI subcommand and the
// MS Teams branch of the `forge init` TUI wizard, so both flows produce
// identical refresh tokens and reuse the same polling / error semantics.
//
// The flow has two halves:
//  1. RequestDeviceCode — POST /devicecode → user_code + verification_uri
//  2. PollDeviceToken    — POST /token repeatedly until the user completes
//     the consent step in their browser
//
// Both halves are network calls; both honour the caller's context for
// cancellation and timeout. OpenURL is a best-effort cross-platform
// browser launcher with the same shape as forge-core/llm/oauth.openBrowser.
package devicecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// DefaultLoginBase is the OAuth 2.0 authority for Microsoft Entra ID's
// commercial cloud. Sovereign clouds override via the LoginBase argument.
const DefaultLoginBase = "https://login.microsoftonline.com"

// DefaultScope is the .default scope marker plus offline_access — the same
// scope set the runtime authManager (forge-plugins/channels/msteams/auth.go)
// requests, so refresh tokens captured by this package are interchangeable
// with ones captured externally.
const DefaultScope = "https://graph.microsoft.com/.default offline_access"

// DeviceCodeResponse is the trimmed Microsoft response to POST /devicecode.
type DeviceCodeResponse struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message,omitempty"`
}

// TokenResponse is the trimmed token endpoint payload.
type TokenResponse struct {
	AccessToken  string `json:"access_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// RequestDeviceCode initiates the device-authorization grant. Returns the
// user_code + verification_uri pair the operator must visit in a browser,
// plus the opaque device_code the caller passes to PollDeviceToken.
func RequestDeviceCode(ctx context.Context, client *http.Client, loginBase, tenant, clientID string) (*DeviceCodeResponse, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if loginBase == "" {
		loginBase = DefaultLoginBase
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", DefaultScope)

	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/devicecode", loginBase, tenant)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("devicecode: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devicecode: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devicecode: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var dc DeviceCodeResponse
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("devicecode: parse response: %w", err)
	}
	if dc.UserCode == "" || dc.DeviceCode == "" || dc.VerificationURI == "" {
		return nil, fmt.Errorf("devicecode: response missing required fields: %s", string(body))
	}
	if dc.Interval < 1 {
		dc.Interval = 5
	}
	return &dc, nil
}

// PollDeviceToken polls the token endpoint until the user completes consent,
// the device code expires, or the context is cancelled. Honours the
// server-advertised interval and the slow_down rate-limit response per
// RFC 8628 §3.5.
//
// clientSecret is optional: pass "" for public-client apps (native/mobile
// registration) and the secret value for confidential-client apps (web
// registration). Entra returns AADSTS7000218 if a confidential client
// omits its secret here, so when in doubt, supply it — public clients
// silently ignore the extra parameter.
func PollDeviceToken(ctx context.Context, client *http.Client, loginBase, tenant, clientID, clientSecret string, dc *DeviceCodeResponse) (*TokenResponse, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if loginBase == "" {
		loginBase = DefaultLoginBase
	}
	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", loginBase, tenant)
	interval := time.Duration(dc.Interval) * time.Second

	for {
		// Wait first — Microsoft rejects the very first poll as
		// authorization_pending anyway, and the spec requires waiting at
		// least `interval` seconds between attempts.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("device-code flow timed out before user completed consent: %w", ctx.Err())
		case <-time.After(interval):
		}

		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", clientID)
		form.Set("device_code", dc.DeviceCode)
		if clientSecret != "" {
			// Required for confidential-client (web) app registrations.
			// Public-client (native) apps tolerate this extra field.
			form.Set("client_secret", clientSecret)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, fmt.Errorf("token: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("token: request: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()

		var tr TokenResponse
		if jerr := json.Unmarshal(body, &tr); jerr != nil {
			return nil, fmt.Errorf("token: parse response (status=%d): %w", resp.StatusCode, jerr)
		}

		switch tr.Error {
		case "":
			if tr.AccessToken == "" {
				return nil, fmt.Errorf("token: response missing access_token: %s", string(body))
			}
			return &tr, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return nil, errors.New("device code expired before user completed consent — restart to get a fresh code")
		case "access_denied":
			return nil, errors.New("user declined the consent prompt")
		default:
			msg := tr.ErrorDesc
			if msg == "" {
				msg = tr.Error
			}
			// Diagnostic: when Entra rejects for missing credentials,
			// surface whether we sent a client_secret. This catches both
			// "secret was empty in our form" and "app is confidential but
			// secret was wrong/expired" cases. Newline-prefixed so it
			// can't be lost to terminal wrapping of the AADSTS message.
			diag := ""
			if strings.Contains(msg, "AADSTS7000218") || strings.Contains(msg, "client_assertion") {
				if clientSecret == "" {
					diag = "\n>> client_secret was NOT sent. Your Entra app is a confidential client; provide MSTEAMS_CLIENT_SECRET."
				} else {
					diag = "\n>> client_secret WAS sent (len=" + lenStr(clientSecret) + ") but Entra still rejected it." +
						"\n>> Most common cause: 'Allow public client flows' is OFF in your Entra app." +
						"\n>>   Fix: Entra portal → App registrations → your app → Authentication →" +
						"\n>>        Advanced settings → 'Allow public client flows' = Yes → Save." +
						"\n>> Less common: the secret VALUE (not the Secret ID) is wrong or expired." +
						"\n>>   Fix: Entra portal → Certificates & secrets → + New client secret → copy the Value column."
				}
			}
			return nil, fmt.Errorf("token endpoint error: %s%s", msg, diag)
		}
	}
}

// lenStr formats an int as a string without bringing in strconv.
func lenStr(s string) string {
	n := len(s)
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// OpenURL launches the host's default browser pointed at u. Best-effort —
// failures (no display, no opener, sandboxed env) are returned so the
// caller can fall back to printing the URL for manual paste.
func OpenURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", u)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
