package runtime

import (
	"os"
	"time"
)

// AuditExportConfig configures the FWS-7 export sinks (issue #95). It
// is intentionally minimal — three knobs only — because each one maps
// to a single CLI flag / env var pair and corresponds to one
// operational decision the deployer has to make:
//
//   - SocketPath: "where does the in-pod sidecar listen?"
//   - HTTPEndpoint: "where does the fallback HTTP receiver listen?"
//   - WriteTimeout: "how long am I willing to spend per emit before
//     dropping?"
//
// Default zero value means "no export sinks; behave exactly like
// pre-FWS-7 (stderr only)." This is the right default because the
// initializ platform deploy receiver injects the env vars; self-managed
// deployments without a sidecar get the unchanged stderr stream.
//
// When both SocketPath and HTTPEndpoint are non-empty, SocketPath wins
// (preferred sink path). The HTTP fallback is purely for environments
// where Unix sockets aren't available — typically Windows containers
// or platform-managed sandboxes that forbid unix:// dialing.
type AuditExportConfig struct {
	// SocketPath is the absolute path to the in-pod Unix Domain Socket
	// the sidecar listens on. Empty disables the socket sink.
	SocketPath string

	// HTTPEndpoint is a localhost URL (e.g. "http://127.0.0.1:9097/v1/audit")
	// the fallback HTTP sink POSTs to. Empty disables the HTTP sink.
	// Ignored when SocketPath is set.
	HTTPEndpoint string

	// WriteTimeout bounds each per-event sink write. Default 50ms.
	// Applies to both the socket and HTTP sinks. The stderr safety-net
	// sink ignores this — stderr writes are bounded by the kernel's
	// pipe buffer, not by us.
	WriteTimeout time.Duration

	// DialTimeout bounds the initial socket dial. Default 1s. Ignored
	// by the HTTP sink (which sets its own per-request timeout to
	// match WriteTimeout).
	DialTimeout time.Duration
}

// Environment variable names. Exposed for `forge run --help` text and
// for the integration test. The CLI in forge-cli/cmd/run.go reads
// these and surfaces matching --audit-* flags; flag wins over env.
const (
	EnvAuditSocket       = "FORGE_AUDIT_SOCKET"
	EnvAuditHTTPEndpoint = "FORGE_AUDIT_HTTP_ENDPOINT"
	EnvAuditWriteTimeout = "FORGE_AUDIT_WRITE_TIMEOUT"
)

// AuditExportConfigFromEnv reads the three env vars and returns a
// populated config. Designed for the case where the CLI flag was not
// set; the caller (`forge run --audit-socket=...`) overrides specific
// fields after this call. WriteTimeout parses Go duration syntax
// ("50ms", "200ms"); a parse failure falls back to default (zero,
// which downstream maps to 50ms).
func AuditExportConfigFromEnv() AuditExportConfig {
	cfg := AuditExportConfig{
		SocketPath:   os.Getenv(EnvAuditSocket),
		HTTPEndpoint: os.Getenv(EnvAuditHTTPEndpoint),
	}
	if v := os.Getenv(EnvAuditWriteTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.WriteTimeout = d
		}
	}
	return cfg
}

// NewAuditLoggerFromConfig constructs an AuditLogger with the standard
// FWS-7 sink stack:
//
//   - stderr safety-net sink (always registered first; the operator
//     can still grep audit NDJSON out of container logs even if the
//     sidecar is down)
//   - socket sink when cfg.SocketPath is set
//   - HTTP sink when cfg.SocketPath is empty and cfg.HTTPEndpoint is
//     set
//
// When both export-sink fields are empty, behavior is identical to
// NewAuditLogger(os.Stderr) — pre-FWS-7 compatibility.
func NewAuditLoggerFromConfig(cfg AuditExportConfig) *AuditLogger {
	sinks := []Sink{newWriterSink(os.Stderr, "stderr")}
	if cfg.SocketPath != "" {
		sinks = append(sinks, NewSocketSink(cfg.SocketPath, cfg.WriteTimeout, cfg.DialTimeout))
	} else if cfg.HTTPEndpoint != "" {
		sinks = append(sinks, NewHTTPSink(cfg.HTTPEndpoint, cfg.WriteTimeout))
	}
	return &AuditLogger{
		sinks:   sinks,
		logOnce: map[string]bool{},
	}
}
