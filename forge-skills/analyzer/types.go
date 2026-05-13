// Package analyzer provides security risk scoring, policy enforcement, and
// audit reporting for forge skills.
package analyzer

// RiskLevel classifies the severity of a risk assessment.
type RiskLevel string

const (
	RiskNone     RiskLevel = "none"
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// RiskScore is a numeric score with a classified risk level.
type RiskScore struct {
	Value int       `json:"value"` // 0-100
	Level RiskLevel `json:"level"`
}

// RiskFactor is a single contributing factor to a risk score.
type RiskFactor struct {
	Category    string `json:"category"` // "egress", "binary", "env", "script"
	Description string `json:"description"`
	Points      int    `json:"points"`
}

// SkillRiskAssessment is the security assessment for a single skill.
type SkillRiskAssessment struct {
	SkillName       string            `json:"skill_name"`
	Score           RiskScore         `json:"score"`
	Factors         []RiskFactor      `json:"factors"`
	Violations      []PolicyViolation `json:"violations,omitempty"`
	Recommendations []string          `json:"recommendations,omitempty"`
}

// PolicyViolation describes a security policy breach.
type PolicyViolation struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // "error", "warning"
	Message  string `json:"message"`
}

// AuditReport is the complete security audit output.
type AuditReport struct {
	Timestamp      string                `json:"timestamp"`
	SkillCount     int                   `json:"skill_count"`
	AggregateScore RiskScore             `json:"aggregate_score"`
	Assessments    []SkillRiskAssessment `json:"assessments"`
	PolicySummary  PolicySummary         `json:"policy_summary"`
}

// PolicySummary aggregates policy violation counts.
type PolicySummary struct {
	TotalViolations int  `json:"total_violations"`
	Errors          int  `json:"errors"`
	Warnings        int  `json:"warnings"`
	Passed          bool `json:"passed"`
}

// SecurityPolicy defines configurable security rules.
//
// Policy-check fields raise PolicyViolations during CheckPolicy. Scoring
// override fields influence how AnalyzeSkill* assigns points — they reduce
// the score for items an operator has explicitly accepted, and the affected
// RiskFactor's Description is annotated with "(via policy)" so the override
// is visible in the audit report.
type SecurityPolicy struct {
	// Policy checks.
	MaxEgressDomains  int      `yaml:"max_egress_domains" json:"max_egress_domains"`
	BinaryDenylist    []string `yaml:"binary_denylist" json:"binary_denylist,omitempty"`
	DeniedEnvPatterns []string `yaml:"denied_env_patterns" json:"denied_env_patterns,omitempty"`
	ScriptPolicy      string   `yaml:"script_policy" json:"script_policy"` // "allow"|"warn"|"deny"
	MaxRiskScore      int      `yaml:"max_risk_score" json:"max_risk_score"`
	MaxTags           int      `yaml:"max_tags" json:"max_tags"`

	// Scoring overrides.
	TrustedDomains   []string `yaml:"trusted_domains" json:"trusted_domains,omitempty"`     // egress domains scored as trusted (+2) instead of unknown (+10)
	AcknowledgedBins []string `yaml:"acknowledged_bins" json:"acknowledged_bins,omitempty"` // builtin high-risk binaries scored as standard (+3) instead of high-risk (+15)
	AcknowledgedEnv  []string `yaml:"acknowledged_env" json:"acknowledged_env,omitempty"`   // env vars matching builtin sensitive patterns scored as standard (+5) instead of sensitive (+10)
}
