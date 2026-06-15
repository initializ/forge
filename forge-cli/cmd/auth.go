package cmd

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/auth"
)

// Operator-facing primitives for the internal bearer token Forge mints
// at agent startup. Same token channel adapters use to call back into
// the A2A endpoint (`Runner.ResolveAuth`, `runner.go:201-225`); reused
// by the K8s scheduler CronJobs in #162. This subcommand exists so
// operators don't have to `cat .forge/runtime.token` or hand-compose
// base64-encoded Secret YAML by hand.
//
// All three subcommands operate on the local agent root resolved from
// --output-dir (the persistent root flag); we don't dial the running
// agent's HTTP API because the typical use cases run BEFORE the agent
// is up (first-deploy bootstrap) or AGAINST the local checkout.

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the agent's internal bearer token",
	Long: `Manage the runtime bearer token Forge mints at agent startup.

The token is stored at <agent-root>/.forge/runtime.token with 0600
permissions and is the same token channel plugins (Slack, Telegram,
MS Teams) use to call back into the A2A endpoint. Scheduled CronJobs
deployed by 'forge package' also consume this token via a Kubernetes
Secret the operator populates out-of-band — never bake the token into
checked-in YAML.`,
}

var authShowTokenCmd = &cobra.Command{
	Use:   "show-token",
	Short: "Print the stored runtime token to stdout",
	Long: `Read the token from <agent-root>/.forge/runtime.token and print
it to stdout. Exits with code 1 and a clear error if the file is
absent.

Typical use: pipe into kubectl to seed a Secret out-of-band before
applying a 'forge package' Deployment.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := agentRootDir()
		tok, err := auth.LoadToken(root)
		if err != nil {
			return fmt.Errorf("reading token: %w", err)
		}
		if tok == "" {
			return fmt.Errorf("no token at %s — run 'forge auth mint-token' first or start the agent once to mint one", auth.TokenPath(root))
		}
		fmt.Println(tok)
		return nil
	},
}

var authMintTokenCmd = &cobra.Command{
	Use:   "mint-token",
	Short: "Generate a fresh runtime token, store it, and print it to stdout",
	Long: `Generate a cryptographically random 256-bit bearer token,
write it to <agent-root>/.forge/runtime.token (0600), and print the
token to stdout for piping into downstream tooling.

Overwrites any existing token at that path. Use this for first-deploy
bootstrap from a clean checkout where the agent has never run and the
runtime.token file does not yet exist.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := agentRootDir()
		tok, err := auth.GenerateToken()
		if err != nil {
			return fmt.Errorf("generating token: %w", err)
		}
		if err := auth.StoreToken(root, tok); err != nil {
			return fmt.Errorf("storing token: %w", err)
		}
		fmt.Println(tok)
		return nil
	},
}

var (
	authSecretYAMLNamespace string
	authSecretYAMLName      string
)

var authSecretYAMLCmd = &cobra.Command{
	Use:   "secret-yaml",
	Short: "Print a Kubernetes Secret YAML containing the runtime token",
	Long: `Print a ready-to-apply Kubernetes Secret manifest holding
the runtime token (loaded from <agent-root>/.forge/runtime.token,
base64-encoded into the data field).

The OPPOSITE of what 'forge package' generates: 'forge package' emits
a credential-less Secret template the operator populates out-of-band.
'forge auth secret-yaml' is the one-liner that does that population
when the operator's deploy doesn't use ExternalSecrets / Sealed
Secrets / SOPS / Vault Agent Injector.

Pipe straight to kubectl:

  forge auth secret-yaml | kubectl apply -f -
  forge auth secret-yaml --namespace prod | kubectl apply -f -

Default name and namespace match what 'forge package' generates:
  --name      defaults to <agent-id>-internal-token
  --namespace defaults to "default"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := agentRootDir()
		tok, err := auth.LoadToken(root)
		if err != nil {
			return fmt.Errorf("reading token: %w", err)
		}
		if tok == "" {
			return fmt.Errorf("no token at %s — run 'forge auth mint-token' first or start the agent once to mint one", auth.TokenPath(root))
		}

		agentID, err := agentIDForSecretName(root)
		if err != nil {
			return err
		}
		name := authSecretYAMLName
		if name == "" {
			name = agentID + "-internal-token"
		}
		ns := authSecretYAMLNamespace
		if ns == "" {
			ns = "default"
		}

		encoded := base64.StdEncoding.EncodeToString([]byte(tok))
		// Hand-rolled YAML — no client-go dep needed for this one
		// command. Matches the manifest 'forge package' produces
		// (#162 Phase 3) exactly so the operator can substitute one
		// for the other without surprising kubectl diffs.
		//
		// forge.agent.id is always sourced from forge.yaml (or the
		// "forge-agent" fallback) — never from the --name override.
		// An operator using --name to clobber the Secret resource
		// name still wants their telemetry / label-selectors keyed
		// on the real agent ID.
		fmt.Printf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    forge.agent.id: %s
type: Opaque
data:
  token: %s
`, name, ns, agentID, encoded)
		return nil
	},
}

// agentRootDir resolves the agent root directory from the persistent
// --output-dir flag. Matches how 'forge run' / 'forge build' / the
// existing secret subcommand treat the root.
func agentRootDir() string {
	if outputDir == "" || outputDir == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return cwd
	}
	abs, err := filepath.Abs(outputDir)
	if err != nil {
		return outputDir
	}
	return abs
}

// agentIDForSecretName best-effort-reads forge.yaml from the agent
// root to derive the default Secret name. Falls back to "forge-agent"
// when the file is missing or the agent_id is not set — the operator
// can always override with --name.
func agentIDForSecretName(root string) (string, error) {
	cfgPath := filepath.Join(root, "forge.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "forge-agent", nil
		}
		return "", fmt.Errorf("reading forge.yaml: %w", err)
	}
	// Light-touch parse: avoid pulling the full ForgeConfig
	// validation chain in just to read a single string. Look for a
	// top-level `agent_id:` line.
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "agent_id:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(trimmed, "agent_id:"))
		v = strings.Trim(v, `"' `)
		if v != "" {
			return v, nil
		}
	}
	return "forge-agent", nil
}

func init() {
	authSecretYAMLCmd.Flags().StringVar(&authSecretYAMLNamespace, "namespace", "", "Kubernetes namespace (default: default)")
	authSecretYAMLCmd.Flags().StringVar(&authSecretYAMLName, "name", "", "Secret name (default: <agent-id>-internal-token)")

	authCmd.AddCommand(authShowTokenCmd)
	authCmd.AddCommand(authMintTokenCmd)
	authCmd.AddCommand(authSecretYAMLCmd)
}
