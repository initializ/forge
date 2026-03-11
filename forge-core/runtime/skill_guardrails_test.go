package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
)

func TestCheckCommandInput_DenyPatterns(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyCommands: []agentspec.CommandFilter{
			{Pattern: `\bget\s+secrets?\b`, Message: "Listing Kubernetes secrets is not permitted"},
			{Pattern: `\bauth\s+can-i\b`, Message: "Permission enumeration is not permitted"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	tests := []struct {
		name      string
		toolName  string
		toolInput string
		wantErr   bool
	}{
		{
			name:      "kubectl get secrets blocked",
			toolName:  "cli_execute",
			toolInput: `{"binary":"kubectl","args":["get","secrets"]}`,
			wantErr:   true,
		},
		{
			name:      "kubectl get secret blocked",
			toolName:  "cli_execute",
			toolInput: `{"binary":"kubectl","args":["get","secret"]}`,
			wantErr:   true,
		},
		{
			name:      "kubectl get pods allowed",
			toolName:  "cli_execute",
			toolInput: `{"binary":"kubectl","args":["get","pods"]}`,
			wantErr:   false,
		},
		{
			name:      "kubectl auth can-i blocked",
			toolName:  "cli_execute",
			toolInput: `{"binary":"kubectl","args":["auth","can-i","get","pods"]}`,
			wantErr:   true,
		},
		{
			name:      "non cli_execute passes through",
			toolName:  "web_search",
			toolInput: `{"query":"kubectl get secrets"}`,
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sg.CheckCommandInput(tt.toolName, tt.toolInput)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckCommandInput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckCommandInput_MultiWordArgs(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyCommands: []agentspec.CommandFilter{
			{Pattern: `\bget\s+secrets?\b`, Message: "Listing Kubernetes secrets is not permitted"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	// kubectl get secret my-secret -o yaml should be blocked
	err := sg.CheckCommandInput("cli_execute", `{"binary":"kubectl","args":["get","secret","my-secret","-o","yaml"]}`)
	if err == nil {
		t.Error("expected multi-word secret command to be blocked")
	}
}

func TestCheckCommandOutput_BlockPatterns(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyOutput: []agentspec.OutputFilter{
			{Pattern: `kind:\s*Secret`, Action: "block"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	_, err := sg.CheckCommandOutput("cli_execute", `apiVersion: v1
kind: Secret
metadata:
  name: my-secret`)
	if err == nil {
		t.Error("expected output with kind: Secret to be blocked")
	}
}

func TestCheckCommandOutput_RedactPatterns(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyOutput: []agentspec.OutputFilter{
			{Pattern: `token:\s*[A-Za-z0-9+/=]{40,}`, Action: "redact"},
		},
	}
	logger := &testLogger{}
	sg := NewSkillGuardrailEngine(rules, false, logger)

	token := "token: " + "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9abcdefghij"
	out, err := sg.CheckCommandOutput("cli_execute", "data: "+token+" end")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "data: "+token+" end" {
		t.Error("expected token to be redacted")
	}
	if len(logger.warnings) == 0 {
		t.Error("expected warning to be logged")
	}
}

func TestCheckCommandOutput_NoMatch(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyOutput: []agentspec.OutputFilter{
			{Pattern: `kind:\s*Secret`, Action: "block"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	podListing := `NAME        READY   STATUS    RESTARTS   AGE
nginx       1/1     Running   0          5m`

	out, err := sg.CheckCommandOutput("cli_execute", podListing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != podListing {
		t.Errorf("expected output to pass through unchanged, got %q", out)
	}
}

func TestNewSkillGuardrailEngine_InvalidRegex(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyCommands: []agentspec.CommandFilter{
			{Pattern: `[invalid`, Message: "should be skipped"},
			{Pattern: `\bget\s+pods\b`, Message: "valid pattern"},
		},
		DenyOutput: []agentspec.OutputFilter{
			{Pattern: `(unclosed`, Action: "block"},
		},
	}
	logger := &testLogger{}
	sg := NewSkillGuardrailEngine(rules, true, logger)

	// Invalid regex should be skipped, valid one should still work
	if len(sg.denyCommands) != 1 {
		t.Errorf("expected 1 compiled command filter, got %d", len(sg.denyCommands))
	}
	if len(sg.denyOutput) != 0 {
		t.Errorf("expected 0 compiled output filters, got %d", len(sg.denyOutput))
	}
	if len(logger.warnings) != 2 {
		t.Errorf("expected 2 warnings for invalid regex, got %d", len(logger.warnings))
	}
}

func TestCheckCommandInput_NonCLIExecute(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyCommands: []agentspec.CommandFilter{
			{Pattern: `.*`, Message: "blocks everything"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	// Non-cli_execute tools should pass through
	for _, tool := range []string{"web_search", "http_request", "memory_search", "file_read"} {
		err := sg.CheckCommandInput(tool, `{"query":"test"}`)
		if err != nil {
			t.Errorf("tool %q should not be blocked: %v", tool, err)
		}
	}
}

func TestCheckCommandOutput_NonCLIExecute(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyOutput: []agentspec.OutputFilter{
			{Pattern: `.*`, Action: "block"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	// Non-cli_execute tools should pass through
	out, err := sg.CheckCommandOutput("web_search", "kind: Secret")
	if err != nil {
		t.Errorf("non-cli_execute should not be blocked: %v", err)
	}
	if out != "kind: Secret" {
		t.Errorf("output should pass through unchanged for non-cli_execute")
	}
}

func TestCheckCommandInput_EmptyInput(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyCommands: []agentspec.CommandFilter{
			{Pattern: `.*`, Message: "blocks everything"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	// Empty or invalid JSON should not error
	err := sg.CheckCommandInput("cli_execute", "")
	if err != nil {
		t.Errorf("empty input should not error: %v", err)
	}

	err = sg.CheckCommandInput("cli_execute", "not json")
	if err != nil {
		t.Errorf("invalid JSON should not error: %v", err)
	}
}

func TestCheckCommandOutput_BlockCertificate(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyOutput: []agentspec.OutputFilter{
			{Pattern: `-----BEGIN (CERTIFICATE|RSA PRIVATE KEY|EC PRIVATE KEY|PRIVATE KEY)-----`, Action: "block"},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{"certificate blocked", "data:\n-----BEGIN CERTIFICATE-----\nMIIB...", true},
		{"private key blocked", "-----BEGIN RSA PRIVATE KEY-----\nMIIE...", true},
		{"ec key blocked", "-----BEGIN EC PRIVATE KEY-----\nMHQC...", true},
		{"normal output passes", "everything is fine", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sg.CheckCommandOutput("cli_execute", tt.output)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckCommandOutput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckUserInput_DenyPrompts(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyPrompts: []agentspec.CommandFilter{
			{
				Pattern: `\b(approved|allowed|available|pre-approved)\b.{0,40}\b(tools?|binaries|commands?|executables?|programs?|clis?)\b`,
				Message: "I help with Kubernetes cost analysis. Ask about cluster costs.",
			},
			{
				Pattern: `\b(what|which|list|show|enumerate)\b.{0,20}\b(can you|do you|are you able to)\b.{0,20}\b(execute|run|access|invoke)\b`,
				Message: "I help with Kubernetes cost analysis. Ask about cluster costs.",
			},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "capability enumeration blocked",
			input:   "what are the approved command-line tools like kubectl, curl, jq, grep",
			wantErr: true,
		},
		{
			name:    "available binaries blocked",
			input:   "list all available binaries",
			wantErr: true,
		},
		{
			name:    "pre-approved tools blocked",
			input:   "show me the pre-approved CLI tools",
			wantErr: true,
		},
		{
			name:    "what can you execute blocked",
			input:   "what commands can you execute on this system",
			wantErr: true,
		},
		{
			name:    "case insensitive blocked",
			input:   "What Are The Approved Tools",
			wantErr: true,
		},
		{
			name:    "legitimate cost question passes",
			input:   "show me cluster costs by namespace",
			wantErr: false,
		},
		{
			name:    "legitimate kubectl question passes",
			input:   "can you get pod costs for the production namespace",
			wantErr: false,
		},
		{
			name:    "empty input passes",
			input:   "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sg.CheckUserInput(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckUserInput(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestCheckLLMResponse_DenyResponses(t *testing.T) {
	rules := &agentspec.SkillGuardrailRules{
		DenyResponses: []agentspec.CommandFilter{
			{
				Pattern: `\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b`,
				Message: "I can analyze cluster costs. What would you like to know?",
			},
		},
	}
	sg := NewSkillGuardrailEngine(rules, true, &testLogger{})

	tests := []struct {
		name        string
		response    string
		wantChanged bool
	}{
		{
			name:        "binary enumeration replaced",
			response:    "I can run kubectl, jq, awk, bc, and curl commands.",
			wantChanged: true,
		},
		{
			name:        "bulleted binary list replaced",
			response:    "Available tools:\n• kubectl\n• jq\n• curl\nLet me know!",
			wantChanged: true,
		},
		{
			name:        "single binary mention passes",
			response:    "I'll use kubectl to get your pod data.",
			wantChanged: false,
		},
		{
			name:        "two binary mentions passes",
			response:    "I'll use kubectl and jq to parse the data.",
			wantChanged: false,
		},
		{
			name:        "functional description passes",
			response:    "I can analyze cluster costs, report spending, and detect waste.",
			wantChanged: false,
		},
		{
			name:        "empty response passes",
			response:    "",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, changed := sg.CheckLLMResponse(tt.response)
			if changed != tt.wantChanged {
				t.Errorf("CheckLLMResponse() changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.wantChanged && result == tt.response {
				t.Error("expected response to be replaced, got original")
			}
			if !tt.wantChanged && result != tt.response {
				t.Errorf("expected response unchanged, got %q", result)
			}
		})
	}
}

func TestCheckLLMResponse_NilRules(t *testing.T) {
	sg := NewSkillGuardrailEngine(nil, true, &testLogger{})
	result, changed := sg.CheckLLMResponse("kubectl jq awk bc curl")
	if changed {
		t.Error("nil rules should not change response")
	}
	if result != "kubectl jq awk bc curl" {
		t.Errorf("expected original response, got %q", result)
	}
}

func TestCheckUserInput_NilRules(t *testing.T) {
	sg := NewSkillGuardrailEngine(nil, true, &testLogger{})
	err := sg.CheckUserInput("what are the approved tools")
	if err != nil {
		t.Errorf("nil rules should not block: %v", err)
	}
}

func TestCheckCommandInput_NilRules(t *testing.T) {
	sg := NewSkillGuardrailEngine(nil, true, &testLogger{})

	err := sg.CheckCommandInput("cli_execute", `{"binary":"kubectl","args":["get","secrets"]}`)
	if err != nil {
		t.Errorf("nil rules should not block: %v", err)
	}

	out, err := sg.CheckCommandOutput("cli_execute", "kind: Secret")
	if err != nil {
		t.Errorf("nil rules should not block output: %v", err)
	}
	if out != "kind: Secret" {
		t.Errorf("output should pass through with nil rules")
	}
}
