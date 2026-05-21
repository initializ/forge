package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-cli/internal/devicecode"
)

// channelMsteamsLoginCmd runs the OAuth 2.0 device-code flow against Entra ID
// and prints a refresh token the operator can paste into MSTEAMS_REFRESH_TOKEN.
//
// The device-code flow has two halves: a user-consent half (visit URL, enter
// code, sign in) and a client-polling half (POST /token repeatedly until the
// user finishes). This command runs both halves so the operator only has to
// do the visible part.
var channelMsteamsLoginCmd = &cobra.Command{
	Use:   "msteams-login",
	Short: "Capture an MS Teams refresh token via the OAuth 2.0 device-code flow",
	Long: `Run the OAuth 2.0 device-code flow against Entra ID to capture a refresh
token for the MS Teams channel adapter.

This command:
  1. Calls /devicecode to obtain a user_code and verification URL
  2. Prints both so you can visit the URL and approve in your browser
  3. Polls /token until you complete the consent
  4. Prints the resulting refresh_token to stdout

Defaults read MSTEAMS_TENANT_ID and MSTEAMS_CLIENT_ID from .env (or the
shell environment) so this works inside an agent project root with no
arguments. Override with --tenant-id and --client-id when needed.

The forge init TUI runs this exact flow inline once you reach the MS Teams
refresh-token step — you do not normally need to run this command yourself.`,
	RunE: runChannelMsteamsLogin,
}

var (
	msteamsLoginTenantID    string
	msteamsLoginClientID    string
	msteamsLoginLoginBase   string
	msteamsLoginTimeoutSecs int
	msteamsLoginWriteEnv    bool
)

func init() {
	channelMsteamsLoginCmd.Flags().StringVar(&msteamsLoginTenantID, "tenant-id", "",
		"Entra tenant ID (defaults to $MSTEAMS_TENANT_ID or the value in .env)")
	channelMsteamsLoginCmd.Flags().StringVar(&msteamsLoginClientID, "client-id", "",
		"Entra app client ID (defaults to $MSTEAMS_CLIENT_ID or the value in .env)")
	channelMsteamsLoginCmd.Flags().StringVar(&msteamsLoginLoginBase, "login-base", devicecode.DefaultLoginBase,
		"OAuth2 authority base URL (override for sovereign clouds: login.microsoftonline.us / login.chinacloudapi.cn)")
	channelMsteamsLoginCmd.Flags().IntVar(&msteamsLoginTimeoutSecs, "timeout-seconds", 900,
		"Maximum time to wait for the user to complete consent (default 900 / 15 minutes)")
	channelMsteamsLoginCmd.Flags().BoolVar(&msteamsLoginWriteEnv, "write-env", false,
		"Append MSTEAMS_REFRESH_TOKEN=<token> to .env in the current directory (instead of printing to stdout)")
	channelCmd.AddCommand(channelMsteamsLoginCmd)
}

func runChannelMsteamsLogin(cmd *cobra.Command, args []string) error {
	tenant := strings.TrimSpace(msteamsLoginTenantID)
	client := strings.TrimSpace(msteamsLoginClientID)

	if tenant == "" || client == "" {
		envFromFile := readEnvFile(".env")
		if tenant == "" {
			tenant = strings.TrimSpace(firstNonEmpty(os.Getenv("MSTEAMS_TENANT_ID"), envFromFile["MSTEAMS_TENANT_ID"]))
		}
		if client == "" {
			client = strings.TrimSpace(firstNonEmpty(os.Getenv("MSTEAMS_CLIENT_ID"), envFromFile["MSTEAMS_CLIENT_ID"]))
		}
	}

	if tenant == "" {
		return errors.New("tenant-id is required: pass --tenant-id or set MSTEAMS_TENANT_ID in .env")
	}
	if client == "" {
		return errors.New("client-id is required: pass --client-id or set MSTEAMS_CLIENT_ID in .env")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(msteamsLoginTimeoutSecs)*time.Second)
	defer cancel()

	httpClient := &http.Client{Timeout: 30 * time.Second}

	dc, err := devicecode.RequestDeviceCode(ctx, httpClient, msteamsLoginLoginBase, tenant, client)
	if err != nil {
		return err
	}

	// User-facing instructions. Stderr so stdout can stay clean for piping.
	stderr := cmd.ErrOrStderr()
	writeln := func(s string) { _, _ = io.WriteString(stderr, s+"\n") }
	writef := func(format string, a ...any) { _, _ = fmt.Fprintf(stderr, format, a...) }

	writeln("")
	writeln("───────────────────────────────────────────────────────────")
	writeln(" To finish signing in, open this URL in a browser:")
	writef("   %s\n", dc.VerificationURI)
	writeln("")
	writeln(" Then enter this one-time code:")
	writef("   %s\n", dc.UserCode)
	writeln("")
	writef(" Code expires in %ds. Polling /token every %ds...\n", dc.ExpiresIn, dc.Interval)
	writeln("───────────────────────────────────────────────────────────")
	writeln("")

	// Best-effort browser launch — failures are silently ignored because
	// the URL is already printed above for manual paste.
	_ = devicecode.OpenURL(dc.VerificationURI)

	tok, err := devicecode.PollDeviceToken(ctx, httpClient, msteamsLoginLoginBase, tenant, client, dc)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("token endpoint returned no refresh_token — did the scope include offline_access?")
	}

	if msteamsLoginWriteEnv {
		if err := appendOrReplaceEnv(".env", "MSTEAMS_REFRESH_TOKEN", tok.RefreshToken); err != nil {
			return fmt.Errorf("writing .env: %w", err)
		}
		_, _ = io.WriteString(cmd.ErrOrStderr(), "✓ MSTEAMS_REFRESH_TOKEN written to .env\n")
		return nil
	}

	// Default: print the token so the operator can paste it. Newline only —
	// no labels — so the output is pipe-friendly.
	_, _ = io.WriteString(cmd.OutOrStdout(), tok.RefreshToken+"\n")
	return nil
}

// --- .env helpers (deliberately tiny — full .env parsing isn't in scope) ---

// readEnvFile returns a key→value map of simple KEY=VALUE lines in path.
// Lines starting with # are ignored. Surrounding quotes are stripped. Returns
// an empty map if the file is absent.
func readEnvFile(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		if key != "" {
			out[key] = val
		}
	}
	return out
}

// appendOrReplaceEnv updates the given key in path's .env-style file. If the
// key already exists (anywhere in the file), the line is replaced; otherwise
// a new line is appended. Creates the file if missing.
func appendOrReplaceEnv(path, key, value string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	existing, _ := os.ReadFile(abs)
	lines := strings.Split(string(existing), "\n")

	newLine := key + "=" + value
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = newLine
			replaced = true
			break
		}
	}
	if !replaced {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines[len(lines)-1] = newLine
			lines = append(lines, "")
		} else {
			lines = append(lines, newLine)
		}
	}
	out := strings.Join(lines, "\n")
	return os.WriteFile(abs, []byte(out), 0o600)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
