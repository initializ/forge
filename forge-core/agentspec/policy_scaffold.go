package agentspec

// PolicyScaffold defines the policy and guardrail configuration for an agent.
type PolicyScaffold struct {
	Guardrails      []Guardrail          `json:"guardrails,omitempty" bson:"guardrails,omitempty" yaml:"guardrails,omitempty"`
	SkillGuardrails *SkillGuardrailRules `json:"skill_guardrails,omitempty" bson:"skill_guardrails,omitempty" yaml:"skill_guardrails,omitempty"`
}

// Guardrail defines a single guardrail rule applied to an agent.
type Guardrail struct {
	Type   string         `json:"type" bson:"type" yaml:"type"`
	Config map[string]any `json:"config,omitempty" bson:"config,omitempty" yaml:"config,omitempty"`
}

// SkillGuardrailRules holds aggregated skill-level deny patterns.
type SkillGuardrailRules struct {
	DenyCommands  []CommandFilter `json:"deny_commands,omitempty"`
	DenyOutput    []OutputFilter  `json:"deny_output,omitempty"`
	DenyPrompts   []CommandFilter `json:"deny_prompts,omitempty"`
	DenyResponses []CommandFilter `json:"deny_responses,omitempty"`
}

// CommandFilter blocks tool execution when the command matches.
type CommandFilter struct {
	Pattern string `json:"pattern"`
	Message string `json:"message"`
}

// OutputFilter blocks or redacts tool output matching a pattern.
type OutputFilter struct {
	Pattern string `json:"pattern"`
	Action  string `json:"action"` // "block" or "redact"
}
