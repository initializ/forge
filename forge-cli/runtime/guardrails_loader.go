package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/initializ/guardrails/models"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// Environment variable names for guardrails resolution hardening
// (issue #166). Mirrors the FORGE_* convention used elsewhere in the
// security subsystem.
const (
	// EnvGuardrailsDB is the existing MongoDB URI selector that
	// activates DB mode. Documented here for grep-ability — the
	// loader still reads the raw string at the FORGE_GUARDRAILS_DB
	// callsite below.
	EnvGuardrailsDB = "FORGE_GUARDRAILS_DB"
	// EnvGuardrailsDBRequired flips the silent-fallback to a
	// startup-time abort when DB mode is selected but Mongo is
	// unreachable. Off by default (back-compat); platform deploys
	// that consider DB mode security-critical should set this to
	// fail loud instead of quietly downgrading to file mode or
	// defaults.
	EnvGuardrailsDBRequired = "FORGE_GUARDRAILS_DB_REQUIRED"
)

// dbFileExclusivityOnce guards the one-shot startup warning emitted
// when both FORGE_GUARDRAILS_DB and a guardrails.json are present.
// Package-scoped so the warning fires exactly once per process even
// if BuildGuardrailChecker is called multiple times (it isn't in
// production, but tests do). Reset in tests via resetDBFileWarning.
var dbFileExclusivityOnce sync.Once

// LoadPolicyScaffold reads policy-scaffold.json from the output directory.
// Returns nil (no error) if the file does not exist.
// Kept for SkillGuardrails loading (separate concern from main guardrails).
func LoadPolicyScaffold(workDir string) (*agentspec.PolicyScaffold, error) {
	path := filepath.Join(workDir, ".forge-output", "policy-scaffold.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ps agentspec.PolicyScaffold
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("parsing policy scaffold: %w", err)
	}
	return &ps, nil
}

// DefaultPolicyScaffold returns a scaffold for SkillGuardrails only.
// The main guardrail checks are now handled by BuildGuardrailChecker.
func DefaultPolicyScaffold() *agentspec.PolicyScaffold {
	return &agentspec.PolicyScaffold{}
}

// BuildGuardrailChecker creates the guardrail engine based on configuration.
// Priority: FORGE_GUARDRAILS_DB env → guardrails.json file → defaults.
//
// auditLogger and auditCfg are wired into the resulting engine so every
// mask/block/warn decision emits a guardrail_check event through the
// same sink stack the A2A handlers use. tracingCfg controls the
// guardrail.<gate> span instrumentation added in #161 — when
// CaptureContent is on, evidence is stamped on the span via the same
// redact-then-truncate pipeline the LLM-call content capture uses.
// When auditLogger is nil the engine is silent on the audit pipeline
// (used by tests).
//
// Issue #166: the function now returns an error rather than always
// falling back to a noop checker. The non-nil-error paths are:
//
//   - FORGE_GUARDRAILS_DB is set, FORGE_GUARDRAILS_DB_REQUIRED is
//     truthy, AND the Mongo connect fails. Without REQUIRED the
//     loader still warns and falls through to file mode for
//     back-compat (the platform deploy should set REQUIRED so a
//     misconfigured Mongo URI fails loud at startup instead of
//     silently downgrading to a less-protective posture).
//
// Other failure paths (file-engine construction error, etc.) continue
// to log and return a NoopGuardrailChecker — they're rare and the
// recovery path is well-understood.
func BuildGuardrailChecker(
	cfg *types.ForgeConfig,
	workDir string,
	enforce bool,
	logger coreruntime.Logger,
	auditLogger *coreruntime.AuditLogger,
	auditCfg GuardrailAuditConfig,
	tracingCfg observability.TracingConfig,
) (coreruntime.GuardrailChecker, error) {
	attach := func(e *LibraryGuardrailEngine) coreruntime.GuardrailChecker {
		if auditLogger != nil {
			e.WithAuditLogger(auditLogger, auditCfg)
		}
		e.WithTracing(tracingCfg)
		return e
	}

	// One-shot exclusivity warning: when DB mode is selected AND a
	// guardrails.json sits in the workdir, the file is silently
	// ignored. Repo readers see a file checked in and assume it's
	// active; in DB-mode deploys it's dead config that drifts. Issue
	// #166. Fires through the ops logger (not the audit stream — this
	// is a config-shape warning, not an audited event), exactly once
	// per process via dbFileExclusivityOnce.
	mongoURI := os.Getenv(EnvGuardrailsDB)
	if mongoURI != "" {
		if filePath, ok := guardrailsFilePathIfPresent(cfg, workDir); ok {
			dbFileExclusivityOnce.Do(func() {
				logger.Warn("guardrails: DB mode active but guardrails.json also present — the file is IGNORED. DB and file are mutually exclusive. Remove the file or unset FORGE_GUARDRAILS_DB to avoid drift.", map[string]any{
					"file": filePath,
				})
			})
		}
	}

	// DB mode: connect to MongoDB for config + audit
	if mongoURI != "" {
		agentID := os.Getenv("FORGE_AGENT_ID")
		if agentID == "" && cfg != nil {
			agentID = cfg.AgentID
		}
		orgID := os.Getenv("FORGE_ORG_ID")
		engine, err := NewDBGuardrailEngine(mongoURI, agentID, orgID, enforce, logger)
		if err == nil {
			logger.Info("guardrails: using MongoDB-backed config", map[string]any{
				"agent_id": agentID,
			})
			return attach(engine), nil
		}
		// FAIL-LOUD path. When REQUIRED is set, the agent refuses
		// to serve rather than quietly downgrade. Platform deploys
		// running guardrails under DB mode should set this in the
		// deployment manifest so a misconfigured URI or a
		// transient Mongo outage doesn't silently downgrade
		// protection.
		if dbModeRequired() {
			logger.Error("guardrails: DB mode required (FORGE_GUARDRAILS_DB_REQUIRED=true) but DB unreachable; refusing to start", map[string]any{
				"error": err.Error(),
			})
			return nil, fmt.Errorf("guardrails DB required but unreachable: %w", err)
		}
		logger.Warn("failed to connect guardrails DB, falling back to file", map[string]any{
			"error": err.Error(),
		})
	}

	// File mode: load from guardrails.json
	sg := LoadGuardrailsJSON(cfg, workDir)
	if sg == nil {
		sg = DefaultStructuredGuardrails()
	}

	engine, err := NewFileGuardrailEngine(sg, enforce, logger)
	if err != nil {
		logger.Warn("failed to create file guardrail engine, using noop", map[string]any{
			"error": err.Error(),
		})
		return &coreruntime.NoopGuardrailChecker{}, nil
	}
	return attach(engine), nil
}

// dbModeRequired reads FORGE_GUARDRAILS_DB_REQUIRED and returns true
// when the operator has opted into fail-loud behavior. Parse failure
// or empty value yields false (back-compat) — same forgiving posture
// as the other guardrails env vars.
func dbModeRequired() bool {
	v := os.Getenv(EnvGuardrailsDBRequired)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

// guardrailsFilePathIfPresent returns the resolved guardrails.json
// path (relative to workDir, honoring cfg.GuardrailsPath) and a bool
// reporting whether the file actually exists on disk. Used by the
// exclusivity warning to give the operator an actionable message
// pointing at the specific file that's being ignored.
func guardrailsFilePathIfPresent(cfg *types.ForgeConfig, workDir string) (string, bool) {
	filename := "guardrails.json"
	if cfg != nil && cfg.GuardrailsPath != "" {
		filename = cfg.GuardrailsPath
	}
	path := filepath.Join(workDir, filename)
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	return path, false
}

// LoadGuardrailsJSON reads guardrails.json from the project directory.
// Returns nil if the file does not exist.
func LoadGuardrailsJSON(cfg *types.ForgeConfig, workDir string) *models.StructuredGuardrails {
	filename := "guardrails.json"
	if cfg != nil && cfg.GuardrailsPath != "" {
		filename = cfg.GuardrailsPath
	}

	path := filepath.Join(workDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var sg models.StructuredGuardrails
	if err := json.Unmarshal(data, &sg); err != nil {
		return nil
	}
	return &sg
}

// DefaultStructuredGuardrails returns default guardrails matching the
// previously built-in patterns (PII, jailbreak, secrets).
func DefaultStructuredGuardrails() *models.StructuredGuardrails {
	return &models.StructuredGuardrails{
		PII: &models.PIIConfig{
			Enabled: true,
			Action:  "mask",
			Categories: map[string]models.PIICategoryConfig{
				"email":       {Enabled: true, Action: "mask"},
				"phoneNumber": {Enabled: true, Action: "mask"},
				"ssn":         {Enabled: true, Action: "mask"},
				"creditCard":  {Enabled: true, Action: "mask"},
			},
		},
		Security: &models.SecurityConfig{
			JailbreakDetection: &models.ThresholdConfig{
				Enabled:             true,
				ConfidenceThreshold: 25,
				Action:              "block",
			},
			PromptInjection: &models.ThresholdConfig{
				Enabled:             true,
				ConfidenceThreshold: 30,
				Action:              "block",
			},
			CommandInjection: &models.ThresholdConfig{
				Enabled:             true,
				ConfidenceThreshold: 35,
				Action:              "block",
			},
		},
		CustomRules: &models.CustomRulesConfig{
			Rules: []models.CustomRule{
				{ID: "secret_anthropic", Name: "Anthropic API Key", Type: "regex", Constraint: "hard", Pattern: `sk-ant-[A-Za-z0-9\-]{20,}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_openai", Name: "OpenAI API Key", Type: "regex", Constraint: "hard", Pattern: `sk-[A-Za-z0-9]{20,}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_github_pat", Name: "GitHub PAT", Type: "regex", Constraint: "hard", Pattern: `ghp_[A-Za-z0-9]{36}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_github_oauth", Name: "GitHub OAuth", Type: "regex", Constraint: "hard", Pattern: `gho_[A-Za-z0-9]{36}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_github_server", Name: "GitHub Server Token", Type: "regex", Constraint: "hard", Pattern: `ghs_[A-Za-z0-9]{36}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_github_fine", Name: "GitHub Fine-grained PAT", Type: "regex", Constraint: "hard", Pattern: `github_pat_[A-Za-z0-9_]{22,}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_aws", Name: "AWS Access Key", Type: "regex", Constraint: "hard", Pattern: `AKIA[0-9A-Z]{16}`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_slack_bot", Name: "Slack Bot Token", Type: "regex", Constraint: "hard", Pattern: `xoxb-[0-9]{10,}-[A-Za-z0-9-]+`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_slack_user", Name: "Slack User Token", Type: "regex", Constraint: "hard", Pattern: `xoxp-[0-9]{10,}-[A-Za-z0-9-]+`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_private_key", Name: "Private Key", Type: "regex", Constraint: "hard", Pattern: `-----BEGIN (RSA|EC|OPENSSH|PRIVATE) .*KEY-----`, Action: "mask", Gates: []string{"output", "tool_call"}},
				{ID: "secret_telegram", Name: "Telegram Bot Token", Type: "regex", Constraint: "hard", Pattern: `[0-9]{8,10}:[A-Za-z0-9_-]{35,}`, Action: "mask", Gates: []string{"output", "tool_call"}},
			},
		},
		GateConfig: &models.GateConfig{
			InputGate:    true,
			ToolCallGate: true,
			OutputGate:   true,
			ContextGate:  false,
			StreamGate:   false,
		},
	}
}
