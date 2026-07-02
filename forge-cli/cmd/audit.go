package cmd

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/spf13/cobra"
)

// auditCmd is the root for `forge audit` subcommands.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Inspect Forge audit logs",
	Long: `Tools for validating Forge NDJSON audit streams.

Every emitted event may carry an Ed25519 signature (Sig + Kid fields)
when the agent has audit signing enabled — see
docs/security/audit-signing.md. ` + "`forge audit verify`" + ` reports the first
integrity failure it encounters.`,
}

var (
	auditVerifyPubKeyFile string
)

var auditVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Verify an NDJSON audit stream (signatures + structural integrity)",
	Long: `Reads an NDJSON audit log line by line, parses each event, and
optionally verifies its Ed25519 signature against a JWKS file.

Exits 0 when the whole stream verifies, non-zero when the first
integrity failure is detected (line number, reason, and best-effort
event body printed).

Use "-" as the file path to read from stdin.
`,
	Args: cobra.ExactArgs(1),
	RunE: auditVerifyRun,
}

func init() {
	auditVerifyCmd.Flags().StringVar(&auditVerifyPubKeyFile, "pubkey", "",
		"path to a JWKS file with the audit signing keys (skip signature verification when absent)")
	auditCmd.AddCommand(auditVerifyCmd)
	rootCmd.AddCommand(auditCmd)
}

func auditVerifyRun(cmd *cobra.Command, args []string) error {
	path := args[0]

	// Load pubkeys if the operator supplied a JWKS.
	opts := coreruntime.VerifyOptions{}
	if auditVerifyPubKeyFile != "" {
		keys, err := loadJWKSFile(auditVerifyPubKeyFile)
		if err != nil {
			return fmt.Errorf("loading pubkeys: %w", err)
		}
		opts.Pubkeys = keys
	}

	var reader *os.File
	if path == "-" {
		reader = os.Stdin
	} else {
		f, err := os.Open(path) //nolint:gosec // operator-supplied path is the intended surface
		if err != nil {
			return fmt.Errorf("opening %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		reader = f
	}

	res, err := coreruntime.VerifyAuditLog(reader, opts)
	if err != nil {
		return fmt.Errorf("reading audit stream: %w", err)
	}

	for _, e := range res.Errors {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", e)
	}

	if res.OK() {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"OK: %d events verified (%d signatures checked)\n",
			res.EventCount, res.SigChecked)
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"FAILED at line %d (event %q)\n"+
			"  reason: %s\n"+
			"  events verified before failure: %d\n",
		res.FirstBadLine, res.BadEvent.Event, res.Reason, res.EventCount-1,
	)
	return fmt.Errorf("audit verify failed at line %d: %s",
		res.FirstBadLine, res.Reason)
}

// loadJWKSFile reads a JSON Web Key Set file and returns a
// kid → Ed25519 pubkey map suitable for VerifyOptions.
func loadJWKSFile(path string) (map[string]ed25519.PublicKey, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied
	if err != nil {
		return nil, err
	}
	var jwks coreruntime.JWKS
	if err := json.Unmarshal(data, &jwks); err != nil {
		return nil, fmt.Errorf("parsing JWKS: %w", err)
	}
	out := make(map[string]ed25519.PublicKey, len(jwks.Keys))
	for i, k := range jwks.Keys {
		pub, err := coreruntime.PublicKeyFromJWK(k)
		if err != nil {
			return nil, fmt.Errorf("JWKS keys[%d]: %w", i, err)
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("JWKS %s has no valid keys", path)
	}
	return out, nil
}
