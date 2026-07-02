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

Every event carries a prev_hash chain link (governance R5 / #212).
When audit signing is enabled the event also carries Kid + Sig
(R6 / #213). ` + "`forge audit verify`" + ` walks the chain and — when a
JWKS is supplied — verifies signatures too. See
docs/security/audit-tamper-evidence.md and audit-signing.md.`,
}

var (
	auditVerifyPubKeyFile string
	auditVerifySkipChain  bool
)

var auditVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Verify an NDJSON audit stream (hash chain + signatures + structural integrity)",
	Long: `Reads an NDJSON audit log line by line and verifies:

  - JSON well-formedness of each event.
  - The prev_hash chain from event to event (R5). The first event
    must carry the genesis hash (64 zeros) or a soft warning is
    printed for head-of-stream truncation.
  - Ed25519 signatures against a JWKS file when --pubkey is supplied
    (R6). Without --pubkey, signed events are walked structurally
    and a warning is printed.

Exits 0 when the whole stream verifies, non-zero when the first
integrity failure is detected (line number, reason, best-effort
event body printed).

Use "-" as the file path to read from stdin.

--skip-chain checks signatures only. Useful for SIEM tail ingestion
where the head of stream is out of view.
`,
	Args: cobra.ExactArgs(1),
	RunE: auditVerifyRun,
}

func init() {
	auditVerifyCmd.Flags().StringVar(&auditVerifyPubKeyFile, "pubkey", "",
		"path to a JWKS file with the audit signing keys (signatures unverified when absent)")
	auditVerifyCmd.Flags().BoolVar(&auditVerifySkipChain, "skip-chain", false,
		"skip prev_hash chain verification (for tail ingestion where head is out of view)")
	auditCmd.AddCommand(auditVerifyCmd)
	rootCmd.AddCommand(auditCmd)
}

func auditVerifyRun(cmd *cobra.Command, args []string) error {
	path := args[0]

	// Load pubkeys if the operator supplied a JWKS.
	opts := coreruntime.VerifyOptions{SkipChain: auditVerifySkipChain}
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
			"OK: %d events verified (%d chain links, %d signatures checked)\n",
			res.EventCount, res.ChainChecked, res.SigChecked)
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
