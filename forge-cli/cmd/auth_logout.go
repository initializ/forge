package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/security"
)

// authLogoutCmd removes a stored LLM OAuth credential so the next sign-in
// prompts again (e.g. to re-show the `forge try` credential picker).
//
// It is deliberately an operator/laptop command and REFUSES to run inside an
// agent runtime (a container, or when FORGE_PLATFORM_TOKEN is injected). A
// deployed agent authenticates with an injected API key or platform token, not
// this OAuth credential store, so there is nothing here for the runtime to log
// out of — and Forge should not be the tool an agent shells out to in order to
// wipe an operator's credential from inside a sandbox. This is defense in
// depth, not the primary control: an agent that can run arbitrary commands
// could `rm` the file directly, so the real mitigation is denying the agent
// write access to the operator's ~/.forge. See PR #353 discussion.
var authLogoutCmd = &cobra.Command{
	Use:   "logout [provider]",
	Short: "Remove a stored LLM OAuth credential (laptop/dev only)",
	Long: `Delete the stored OAuth credential for an LLM provider (default: openai)
from ~/.forge/credentials and the encrypted store, so the next
'forge init' / 'forge try' prompts you to sign in again.

This is an operator/laptop command. It refuses to run inside an agent
runtime (a container, or when FORGE_PLATFORM_TOKEN is set): a deployed
agent authenticates with an injected API key or platform token, not
this OAuth file, so there is nothing there to log out of.`,
	Args:         cobra.MaximumNArgs(1),
	RunE:         runAuthLogout,
	SilenceUsage: true,
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	provider := "openai"
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		provider = strings.ToLower(strings.TrimSpace(args[0]))
	}

	// Validate against the known providers BEFORE the value reaches the
	// credential store, whose provider->path mapping is
	// filepath.Join(dir, provider+".json") with no sanitization — so an
	// unchecked arg like "../../../../tmp/x" would delete /tmp/x.json. Same
	// whitelist the paste-key path uses.
	if _, ok := providerKeyEnv[provider]; !ok {
		return fmt.Errorf("unknown provider %q (use openai, anthropic, or gemini)", provider)
	}

	if reason, denied := deniedInAgentRuntime(); denied {
		return fmt.Errorf("refusing to log out %s: %s. "+
			"`auth logout` is a laptop/dev command; a deployed agent uses an injected "+
			"key or platform token, not the OAuth credential store", provider, reason)
	}

	out := cmd.OutOrStdout()
	// Only report "nothing to do" when the store is DEFINITIVELY empty. On a
	// read error (e.g. a corrupt token file) fall through to delete so logout
	// still clears it, rather than silently leaving it in place.
	if tok, err := oauth.LoadCredentials(provider); err == nil && tok == nil {
		_, _ = fmt.Fprintf(out, "No %s credential stored; nothing to do.\n", provider)
		return nil
	}
	if err := oauth.DeleteCredentials(provider); err != nil {
		return fmt.Errorf("removing %s credentials: %w", provider, err)
	}
	_, _ = fmt.Fprintf(out, "Logged out of %s. The next sign-in will prompt again.\n", provider)
	return nil
}

// deniedInAgentRuntime reports whether the process looks like a deployed agent
// runtime (a container, or a managed deployment with a platform token) rather
// than an operator's laptop, along with a human reason. Sensitive operator-only
// auth commands refuse there.
func deniedInAgentRuntime() (reason string, denied bool) {
	if os.Getenv("FORGE_PLATFORM_TOKEN") != "" {
		return "FORGE_PLATFORM_TOKEN is set (managed deployment)", true
	}
	if security.InContainer() {
		return "running inside a container", true
	}
	return "", false
}
