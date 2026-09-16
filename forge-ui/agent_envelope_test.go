package forgeui

import "testing"

func TestParseAgentEnvelope_FullDraft(t *testing.T) {
	resp := `{"message":"Here's your agent.","agent":{"name":"PR Reviewer","model_provider":"openai","model_name":"gpt-5.4","builtin_tools":["web_fetch"],"skills":["github-pr-review"],"system_prompt":"You review PRs."}}`
	msg, agent, structured := parseAgentEnvelope(resp)
	if !structured {
		t.Fatalf("structured = false, want true")
	}
	if msg != "Here's your agent." {
		t.Errorf("message = %q", msg)
	}
	if agent == nil {
		t.Fatalf("agent is nil")
	}
	if agent.Name != "PR Reviewer" || agent.ModelProvider != "openai" || agent.ModelName != "gpt-5.4" {
		t.Errorf("agent fields wrong: %+v", agent)
	}
	if len(agent.BuiltinTools) != 1 || agent.BuiltinTools[0] != "web_fetch" {
		t.Errorf("builtin_tools = %v", agent.BuiltinTools)
	}
	if len(agent.Skills) != 1 || agent.Skills[0] != "github-pr-review" {
		t.Errorf("skills = %v", agent.Skills)
	}
	if agent.SystemPrompt != "You review PRs." {
		t.Errorf("system_prompt = %q", agent.SystemPrompt)
	}
}

func TestParseAgentEnvelope_InterviewingNullAgent(t *testing.T) {
	resp := `{"message":"Which model should it run on?","agent":null}`
	msg, agent, structured := parseAgentEnvelope(resp)
	if !structured {
		t.Fatalf("structured = false, want true")
	}
	if agent != nil {
		t.Errorf("agent = %+v, want nil while interviewing", agent)
	}
	if msg != "Which model should it run on?" {
		t.Errorf("message = %q", msg)
	}
}

func TestParseAgentEnvelope_EnvelopeAfterProse(t *testing.T) {
	// A model may emit a ```json fence or prose before the object; the
	// first balanced object that carries both keys should win.
	resp := "Sure! Here is the spec:\n```json\n{\"message\":\"done\",\"agent\":{\"name\":\"X\",\"model_provider\":\"ollama\",\"model_name\":\"llama3\"}}\n```"
	msg, agent, structured := parseAgentEnvelope(resp)
	if !structured {
		t.Fatalf("structured = false, want true")
	}
	if agent == nil || agent.Name != "X" || agent.ModelProvider != "ollama" {
		t.Errorf("agent parsed wrong: %+v", agent)
	}
	if msg != "done" {
		t.Errorf("message = %q", msg)
	}
}

func TestParseAgentEnvelope_NonEnvelopeFallsBackToRaw(t *testing.T) {
	resp := "I cannot build that."
	msg, agent, structured := parseAgentEnvelope(resp)
	if structured {
		t.Errorf("structured = true, want false for non-envelope text")
	}
	if agent != nil {
		t.Errorf("agent = %+v, want nil", agent)
	}
	if msg != "I cannot build that." {
		t.Errorf("message = %q", msg)
	}
}

// A stray {"message":"ok"} in prose (without an "agent" key) must not
// hijack the parse — looksLikeAgentEnvelope requires both keys.
func TestParseAgentEnvelope_IgnoresIncidentalMessageObject(t *testing.T) {
	resp := `The tool returned {"message":"ok"} but that is not my answer.`
	_, agent, structured := parseAgentEnvelope(resp)
	if structured {
		t.Errorf("structured = true, want false — incidental object must not match")
	}
	if agent != nil {
		t.Errorf("agent = %+v, want nil", agent)
	}
}
