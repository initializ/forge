package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/initializ/forge/forge-cli/internal/wapair"
	corechannels "github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/whatsapp"
)

// channelWhatsappLoginCmd pairs a WhatsApp account with this agent by linking
// it as a WhatsApp Web device.
//
// The flow has two halves, like the MS Teams device-code login: a user half
// (scan the QR code on the phone) and a client half (hold the socket open
// until WhatsApp confirms the pairing). This command runs both so the operator
// only has to do the visible part.
var channelWhatsappLoginCmd = &cobra.Command{
	Use:   "whatsapp-login",
	Short: "Pair a WhatsApp account by scanning a QR code",
	Long: `Link a WhatsApp account to this agent by scanning a QR code, the same way
you link WhatsApp Web or the WhatsApp desktop app.

This command:
  1. Opens (creating if absent) the session store
  2. Renders a QR code in the terminal, refreshing it as each one expires
  3. Waits for you to scan it from the phone
  4. Persists the paired session so ` + "`forge run --with whatsapp`" + ` can connect

On the phone: WhatsApp → Settings → Linked Devices → Link a Device.

The session store is written to the session_path in whatsapp-config.yaml
(default .forge/channels/whatsapp-session.db). That file IS the credential —
anyone holding it can send messages as the linked account. Keep it out of
version control.

WARNING: this uses the WhatsApp Web protocol, not the official WhatsApp Cloud
API. Automating it is against WhatsApp's Terms of Service and can get the
linked number banned. Pair a dedicated number, never a personal one.`,
	RunE: runChannelWhatsappLogin,
}

var (
	whatsappLoginSessionPath string
	whatsappLoginTimeoutSecs int
	whatsappLoginForce       bool
)

func init() {
	channelWhatsappLoginCmd.Flags().StringVar(&whatsappLoginSessionPath, "session-path", "",
		"Path to the session store (defaults to session_path in whatsapp-config.yaml, else .forge/channels/whatsapp-session.db)")
	channelWhatsappLoginCmd.Flags().IntVar(&whatsappLoginTimeoutSecs, "timeout-seconds", 300,
		"Maximum time to wait for the QR code to be scanned (default 300 / 5 minutes)")
	channelWhatsappLoginCmd.Flags().BoolVar(&whatsappLoginForce, "force", false,
		"Re-pair even if the session store already holds a paired account")
	channelCmd.AddCommand(channelWhatsappLoginCmd)
}

func runChannelWhatsappLogin(cmd *cobra.Command, args []string) error {
	sessionPath := resolveWhatsappSessionPath(whatsappLoginSessionPath)

	stderr := cmd.ErrOrStderr()
	writeln := func(s string) { _, _ = io.WriteString(stderr, s+"\n") }
	writef := func(format string, a ...any) { _, _ = fmt.Fprintf(stderr, format, a...) }

	// A paired session already present is almost always the operator running
	// this twice, not a request to re-pair. Re-pairing silently would revoke
	// the working link, so require an explicit --force.
	if !whatsappLoginForce && whatsapp.SessionExists(cmd.Context(), sessionPath) {
		writef("A paired WhatsApp session already exists at %s\n", sessionPath)
		writeln("Pass --force to discard it and pair again.")
		return nil
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(whatsappLoginTimeoutSecs)*time.Second)
	defer cancel()

	// A --force re-pair must start from a clean store: whatsmeow will not
	// re-issue QR codes for a device row left half-registered by the previous
	// pairing.
	if whatsappLoginForce {
		if err := os.Remove(sessionPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("whatsapp: clearing existing session: %w", err)
		}
	}

	session, err := wapair.Start(ctx, sessionPath)
	if err != nil {
		return err
	}
	defer session.Close() //nolint:errcheck

	writeln("")
	writeln("───────────────────────────────────────────────────────────")
	writeln(" On your phone: WhatsApp → Settings → Linked Devices →")
	writeln(" Link a Device, then scan this code:")
	writeln("───────────────────────────────────────────────────────────")

	for evt := range session.Events() {
		switch evt.Kind {
		case wapair.EventQR:
			wapair.RenderQR(evt.Code, stderr)
			writef(" Code expires in %s — a new one will appear automatically.\n", evt.Timeout.Round(time.Second))

		case wapair.EventScanned:
			writeln("")
			writeln(" Scanned — finalizing the link with WhatsApp...")
			writeln(" (Do not interrupt; the pairing is not complete yet.)")

		case wapair.EventPaired:
			writeln("")
			writeln("✓ Paired.")
			if evt.JID != "" {
				writef("  Account:  %s\n", evt.JID)
			}
			writef("  Session:  %s\n", sessionPath)
			writeln("")
			writeln("  This file is the credential — keep it out of version control.")
			writeln("  Start the agent with: forge run --with whatsapp")
			return nil

		case wapair.EventError:
			// A deadline here is the operator not scanning in time, not a
			// protocol failure — say so in those terms.
			if errors.Is(evt.Err, context.DeadlineExceeded) {
				return fmt.Errorf("whatsapp: timed out after %ds waiting for the QR code to be scanned — re-run with --timeout-seconds to allow longer",
					whatsappLoginTimeoutSecs)
			}
			return fmt.Errorf("whatsapp: %w", evt.Err)
		}
	}

	return errors.New("whatsapp: pairing ended without completing — re-run `forge channel whatsapp-login` to retry")
}

// resolveWhatsappSessionPath picks the session store location: the --session-path
// flag, else session_path from whatsapp-config.yaml in the working directory,
// else the adapter default. Relative paths resolve against the working
// directory so the login command and the adapter agree on the location.
func resolveWhatsappSessionPath(flagValue string) string {
	path := strings.TrimSpace(flagValue)
	if path == "" {
		path = whatsappSessionPathFromConfig("whatsapp-config.yaml")
	}
	if path == "" {
		path = ".forge/channels/whatsapp-session.db"
	}
	if filepath.IsAbs(path) {
		return path
	}
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	return filepath.Join(wd, path)
}

// whatsappSessionPathFromConfig reads session_path out of a channel config,
// resolving the _env indirection the way the adapter does. Returns "" when the
// file is absent or holds no session_path — the caller falls back to the
// default.
func whatsappSessionPathFromConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg corechannels.ChannelConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(corechannels.ResolveEnvVars(&cfg)["session_path"])
}
