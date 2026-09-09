package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-cli/internal/tui/steps"
)

// whatsappSessionRelativePath is where the adapter expects the paired session,
// matching session_path in whatsapp-config.yaml.tmpl.
const whatsappSessionRelativePath = ".forge/channels/whatsapp-session.db"

// relocateWhatsappSession moves a session paired during the init wizard into
// the freshly scaffolded project.
//
// The wizard pairs before the project directory exists, so it writes to a temp
// store and passes the path through a synthetic env key. This consumes that
// key — the path must never reach the generated .env, where it would both leak
// a temp path and be wrong the moment the temp dir is cleaned.
//
// Failure is reported but not fatal: the project is already scaffolded and
// usable, and the operator can re-pair with `forge channel whatsapp-login`.
func relocateWhatsappSession(opts *initOptions, dir string) (paired bool, err error) {
	src := opts.EnvVars[steps.WhatsappSessionTokenKey]
	delete(opts.EnvVars, steps.WhatsappSessionTokenKey)
	if src == "" {
		return false, nil
	}
	// The temp dir is ours to remove either way — a failed copy leaves nothing
	// worth keeping behind.
	defer os.RemoveAll(filepath.Dir(src)) //nolint:errcheck

	if _, err := os.Stat(src); err != nil {
		return false, fmt.Errorf("paired session missing at %s: %w", src, err)
	}

	dst := filepath.Join(dir, whatsappSessionRelativePath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, fmt.Errorf("creating session directory: %w", err)
	}
	// copyFileMode (cmd/skill_import.go) honours the umask, so a restrictive
	// mode can come out looser than asked. This file is the WhatsApp
	// credential, so force it afterwards.
	//
	// A rename would be cheaper than a copy but cannot be relied on: the temp
	// dir and the project often sit on different filesystems (/var/folders vs
	// $HOME on macOS, tmpfs vs the workspace in a container), where rename
	// fails with EXDEV.
	if err := copyFileMode(src, dst, 0o600); err != nil {
		return false, fmt.Errorf("writing session to %s: %w", dst, err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return false, fmt.Errorf("securing session file %s: %w", dst, err)
	}
	return true, nil
}
