package agentspec

// PolicyScaffold defines the policy and guardrail configuration for an agent.
type PolicyScaffold struct {
	Guardrails []Guardrail `json:"guardrails,omitempty" bson:"guardrails,omitempty" yaml:"guardrails,omitempty"`
}

// Guardrail defines a single guardrail rule applied to an agent.
type Guardrail struct {
	Type   string         `json:"type" bson:"type" yaml:"type"`
	Config map[string]any `json:"config,omitempty" bson:"config,omitempty" yaml:"config,omitempty"`
}
