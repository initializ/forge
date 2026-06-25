package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/observability"
	"github.com/initializ/forge/forge-core/types"
)

// resetDBFileWarning lets a test re-arm the one-shot exclusivity
// warning between assertions. Lives only in _test.go so production
// callers can't accidentally rearm it. Issue #166.
func resetDBFileWarning() {
	dbFileExclusivityOnce = sync.Once{}
}

// captureLogger records the highest-severity message emitted plus
// every Warn/Error line so tests can assert what surfaced through
// the ops logger (NOT the audit stream — the exclusivity warning
// and the fail-loud abort both go through Logger, never the
// AuditLogger).
type captureLogger struct {
	infos  []string
	warns  []string
	errors []string
}

func (l *captureLogger) Debug(msg string, _ map[string]any) {}
func (l *captureLogger) Info(msg string, _ map[string]any)  { l.infos = append(l.infos, msg) }
func (l *captureLogger) Warn(msg string, _ map[string]any)  { l.warns = append(l.warns, msg) }
func (l *captureLogger) Error(msg string, _ map[string]any) { l.errors = append(l.errors, msg) }

// TestBuildGuardrailChecker_DBRequired_FailsLoudOnUnreachable is the
// core issue #166 invariant: with FORGE_GUARDRAILS_DB pointing at an
// unreachable host AND FORGE_GUARDRAILS_DB_REQUIRED=true, startup
// MUST return a non-nil error rather than quietly downgrading to
// file mode or defaults. The runner propagates the error so the
// agent process exits non-zero and the platform deploy can surface
// the failure.
func TestBuildGuardrailChecker_DBRequired_FailsLoudOnUnreachable(t *testing.T) {
	resetDBFileWarning()
	// 127.0.0.1:1 is reliably unreachable on test hosts.
	t.Setenv(EnvGuardrailsDB, "mongodb://127.0.0.1:1/forbidden")
	t.Setenv(EnvGuardrailsDBRequired, "true")

	logger := &captureLogger{}
	checker, err := BuildGuardrailChecker(nil, t.TempDir(), false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})

	if err == nil {
		t.Fatalf("expected error in REQUIRED mode with unreachable DB; got checker=%v", checker)
	}
	if checker != nil {
		t.Errorf("REQUIRED-mode failure must return nil checker; got %T", checker)
	}
	if !strings.Contains(err.Error(), "DB required but unreachable") {
		t.Errorf("error must mention REQUIRED-mode; got %q", err.Error())
	}
	// Logger should carry an Error line (not just Warn) so platform
	// ops can grep for the specific failure mode.
	if len(logger.errors) == 0 {
		t.Errorf("expected an Error log line in REQUIRED-mode failure; got warns=%v", logger.warns)
	}
}

// TestBuildGuardrailChecker_DBUnreachable_FallsBackByDefault pins
// the back-compat path: with DB set but REQUIRED unset, an
// unreachable DB still warn-and-falls-back to file mode. Operators
// who deliberately want the safer fail-loud posture set REQUIRED.
func TestBuildGuardrailChecker_DBUnreachable_FallsBackByDefault(t *testing.T) {
	resetDBFileWarning()
	t.Setenv(EnvGuardrailsDB, "mongodb://127.0.0.1:1/forbidden")
	// REQUIRED deliberately unset.

	logger := &captureLogger{}
	checker, err := BuildGuardrailChecker(nil, t.TempDir(), false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if err != nil {
		t.Fatalf("default (non-REQUIRED) path must not error on DB unreachable; got %v", err)
	}
	if checker == nil {
		t.Errorf("default path must return a non-nil fallback checker")
	}
	// At least one Warn should describe the fallback. Tests are
	// loose on the exact message wording so a future log tweak
	// doesn't break the invariant.
	foundWarn := false
	for _, w := range logger.warns {
		if strings.Contains(w, "guardrails") || strings.Contains(w, "fall") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected a fallback warning; got warns=%v", logger.warns)
	}
}

// TestBuildGuardrailChecker_DBRequiredAcceptsForgivingParse confirms
// the env-parse posture matches the rest of the FORGE_* env layer:
// garbage values fall back to "not set" (safe default — same as
// AuditPayloadCaptureFromEnv). A typo in FORGE_GUARDRAILS_DB_REQUIRED
// doesn't accidentally enable fail-loud mode in deployments that
// didn't intend to opt in.
func TestBuildGuardrailChecker_DBRequiredAcceptsForgivingParse(t *testing.T) {
	resetDBFileWarning()
	t.Setenv(EnvGuardrailsDB, "mongodb://127.0.0.1:1/forbidden")
	t.Setenv(EnvGuardrailsDBRequired, "not-a-bool")

	logger := &captureLogger{}
	_, err := BuildGuardrailChecker(nil, t.TempDir(), false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if err != nil {
		t.Errorf("malformed REQUIRED must not flip fail-loud; got err=%v", err)
	}
}

// TestBuildGuardrailChecker_DBAndFile_WarnsOnce is the Part 3
// invariant: when an operator has BOTH FORGE_GUARDRAILS_DB set AND a
// guardrails.json checked into the workdir, the file is silently
// ignored today — repo readers see it and assume it's active. The
// one-shot warning makes the dead-config drift visible without
// spamming the log on every BuildGuardrailChecker call.
func TestBuildGuardrailChecker_DBAndFile_WarnsOnce(t *testing.T) {
	resetDBFileWarning()
	wd := t.TempDir()
	guardrailsPath := filepath.Join(wd, "guardrails.json")
	if err := os.WriteFile(guardrailsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvGuardrailsDB, "mongodb://127.0.0.1:1/forbidden")

	logger := &captureLogger{}
	// First call — warning should fire.
	_, _ = BuildGuardrailChecker(nil, wd, false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	exclusivityWarns := countWarnsContaining(logger.warns, "DB and file are mutually exclusive")
	if exclusivityWarns != 1 {
		t.Errorf("expected exactly 1 exclusivity warning on first build; got %d (warns=%v)",
			exclusivityWarns, logger.warns)
	}

	// Second call — warning must NOT re-fire. One-shot semantics so
	// the message doesn't spam ops logs in tests that build the
	// checker repeatedly or in any code path that ever calls
	// BuildGuardrailChecker more than once.
	logger2 := &captureLogger{}
	_, _ = BuildGuardrailChecker(nil, wd, false, logger2, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if countWarnsContaining(logger2.warns, "DB and file are mutually exclusive") != 0 {
		t.Errorf("exclusivity warning re-fired on second build; got warns=%v", logger2.warns)
	}
}

// TestBuildGuardrailChecker_DBOnly_NoFile_NoExclusivityWarn confirms
// the warning ONLY fires when both are present. A clean DB-mode
// deploy with no leftover guardrails.json must not see a spurious
// "file is ignored" line.
func TestBuildGuardrailChecker_DBOnly_NoFile_NoExclusivityWarn(t *testing.T) {
	resetDBFileWarning()
	t.Setenv(EnvGuardrailsDB, "mongodb://127.0.0.1:1/forbidden")

	logger := &captureLogger{}
	_, _ = BuildGuardrailChecker(nil, t.TempDir(), false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if countWarnsContaining(logger.warns, "DB and file are mutually exclusive") != 0 {
		t.Errorf("exclusivity warning fired without guardrails.json; got warns=%v", logger.warns)
	}
}

// TestBuildGuardrailChecker_FileOnly_NoWarn confirms the file-only
// path is unchanged — no DB env, no exclusivity warning.
func TestBuildGuardrailChecker_FileOnly_NoWarn(t *testing.T) {
	resetDBFileWarning()
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "guardrails.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// FORGE_GUARDRAILS_DB deliberately unset via Setenv("").
	t.Setenv(EnvGuardrailsDB, "")

	logger := &captureLogger{}
	_, _ = BuildGuardrailChecker(nil, wd, false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if countWarnsContaining(logger.warns, "DB and file are mutually exclusive") != 0 {
		t.Errorf("exclusivity warning fired in file-only mode; got warns=%v", logger.warns)
	}
}

// TestBuildGuardrailChecker_HonorsCustomGuardrailsPath confirms the
// exclusivity check resolves cfg.GuardrailsPath, not just the
// default `guardrails.json` filename. Operators who renamed the
// file via forge.yaml must still see the warning when it conflicts
// with DB mode.
func TestBuildGuardrailChecker_HonorsCustomGuardrailsPath(t *testing.T) {
	resetDBFileWarning()
	wd := t.TempDir()
	customPath := "policies/custom-guardrails.json"
	if err := os.MkdirAll(filepath.Join(wd, "policies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, customPath), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvGuardrailsDB, "mongodb://127.0.0.1:1/forbidden")

	cfg := &types.ForgeConfig{GuardrailsPath: customPath}
	logger := &captureLogger{}
	_, _ = BuildGuardrailChecker(cfg, wd, false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if countWarnsContaining(logger.warns, "DB and file are mutually exclusive") != 1 {
		t.Errorf("expected exclusivity warning for custom GuardrailsPath; got warns=%v", logger.warns)
	}
}

func countWarnsContaining(warns []string, substr string) int {
	n := 0
	for _, w := range warns {
		if strings.Contains(w, substr) {
			n++
		}
	}
	return n
}
