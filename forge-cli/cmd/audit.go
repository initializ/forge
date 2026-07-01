package cmd

import (
	"fmt"
	"os"
	"strings"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/spf13/cobra"
)

// auditCmd is the root for `forge audit` subcommands. Phase 1 (#212)
// ships `verify`; future phases add `export`, `search`, and signature
// verification (see #213).
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Inspect Forge audit logs",
	Long: `Tools for validating Forge NDJSON audit streams.

The audit pipeline emits each event with a sha256 hash chain over the
previous event (prev_hash field). ` + "`forge audit verify`" + ` walks a captured
stream forward and reports any tampering.`,
}

var auditVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Verify the sha256 hash chain of an NDJSON audit stream",
	Long: `Reads an NDJSON audit log line by line, recomputes each event's
sha256 canonical-JSON hash, and asserts the next event carries that
value in its prev_hash field.

Exits 0 when the whole stream verifies, 1 when a tampering point is
detected (the first bad event's line number, expected vs actual
prev_hash, and best-effort event body are printed).

Use "-" as the file path to read from stdin — useful in pipelines:

    docker logs my-agent | jq -c 'select(.event)' | forge audit verify -
`,
	Args: cobra.ExactArgs(1),
	RunE: auditVerifyRun,
}

func init() {
	auditCmd.AddCommand(auditVerifyCmd)
	rootCmd.AddCommand(auditCmd)
}

func auditVerifyRun(cmd *cobra.Command, args []string) error {
	path := args[0]
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

	res, err := coreruntime.VerifyAuditLog(reader)
	if err != nil {
		return fmt.Errorf("reading audit stream: %w", err)
	}

	// Non-fatal issues — collected during walk, printed here for
	// operator context. Genesis absence is a warning, not a hard fail
	// (a mid-stream capture legitimately won't start at genesis).
	if len(res.Errors) > 0 {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warnings:")
		for _, e := range res.Errors {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "  -", e)
		}
	}
	if res.EventCount > 0 && !res.GenesisSeen && res.OK() {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "note: stream did not start at genesis "+
			"(prev_hash="+AuditChainGenesisAbbrev()+") — mid-stream capture is fine, "+
			"but this cannot detect head-truncation")
	}

	if res.OK() {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "OK: %d events, hash chain intact\n", res.EventCount)
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"TAMPERING DETECTED at line %d (event %q)\n"+
			"  expected prev_hash: %s\n"+
			"  actual prev_hash:   %s\n"+
			"  events verified before break: %d\n",
		res.FirstTamperedLine,
		res.TamperedEvent.Event,
		abbrev(res.ExpectedPrevHash),
		abbrev(res.ActualPrevHash),
		res.EventCount-1,
	)
	return fmt.Errorf("audit stream tampering detected at line %d", res.FirstTamperedLine)
}

// AuditChainGenesisAbbrev returns a short-form of the genesis hash
// for human-readable output (full value is 64 chars of zeros).
func AuditChainGenesisAbbrev() string {
	return coreruntime.AuditChainGenesis[:8] + "…"
}

// abbrev shortens a 64-char hex hash to something readable in a
// terminal without losing enough info to correlate to logs.
func abbrev(hash string) string {
	if hash == "" {
		return "(empty)"
	}
	if strings.EqualFold(hash, coreruntime.AuditChainGenesis) {
		return "genesis"
	}
	if len(hash) <= 16 {
		return hash
	}
	return hash[:8] + "…" + hash[len(hash)-8:]
}
