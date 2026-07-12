package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/guardrails/models"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

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

// BuildGuardrailChecker creates the guardrail engine from guardrails.json
// (or the built-in defaults when no file is present), then applies the
// platform guardrails overlay (#284) so an operator can further restrict
// the agent's guardrails without editing the agent's file.
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
// A file-engine construction error logs and returns a
// NoopGuardrailChecker — rare, and the recovery path is well-understood.
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

	// Load the agent's guardrails.json (or the built-in defaults).
	sg := LoadGuardrailsJSON(cfg, workDir)
	if sg == nil {
		sg = DefaultStructuredGuardrails()
	}

	// Platform guardrails overlay (#284): a workspace/user/system operator
	// can FURTHER RESTRICT the agent's guardrails — force detections/gates
	// on, raise actions, lower thresholds, union rule/denylist/blocked-skill
	// sets. It can never loosen. Applied to whichever config we resolved
	// (file or defaults) so the tightening is universal. Fail-closed: a
	// malformed overlay aborts startup rather than dropping the mandate.
	sg, err := applyPlatformGuardrailsOverlay(sg, logger)
	if err != nil {
		return nil, fmt.Errorf("platform guardrails overlay: %w", err)
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
