package analyzer

import (
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestAnalyzeSkillDescriptor_NoRisk(t *testing.T) {
	sd := &contract.SkillDescriptor{Name: "simple"}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 0 {
		t.Fatalf("expected score 0, got %d", a.Score.Value)
	}
	if a.Score.Level != RiskNone {
		t.Fatalf("expected level none, got %s", a.Score.Level)
	}
}

func TestAnalyzeSkillDescriptor_TrustedDomain(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:          "github",
		EgressDomains: []string{"api.github.com"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 2 {
		t.Fatalf("expected score 2, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_UnknownDomain(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:          "custom",
		EgressDomains: []string{"evil.example.com"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 10 {
		t.Fatalf("expected score 10, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_HighRiskBinary(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:         "shell-tool",
		RequiredBins: []string{"bash"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 15 {
		t.Fatalf("expected score 15, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_StandardBinary(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:         "api-tool",
		RequiredBins: []string{"curl"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 3 {
		t.Fatalf("expected score 3, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_SensitiveEnv(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:        "secret-tool",
		RequiredEnv: []string{"AWS_SECRET_ACCESS_KEY"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 10 {
		t.Fatalf("expected score 10, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_StandardAPIKey(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:        "api-tool",
		RequiredEnv: []string{"GH_TOKEN"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	if a.Score.Value != 5 {
		t.Fatalf("expected score 5, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_WithScript(t *testing.T) {
	sd := &contract.SkillDescriptor{Name: "scripted"}
	a := AnalyzeSkillDescriptor(sd, true)

	if a.Score.Value != 20 {
		t.Fatalf("expected score 20, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_Combined(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:          "github",
		EgressDomains: []string{"api.github.com", "github.com"},
		RequiredBins:  []string{"gh"},
		RequiredEnv:   []string{"GH_TOKEN"},
	}
	a := AnalyzeSkillDescriptor(sd, false)

	// 2 + 2 + 3 + 5 = 12
	expected := 12
	if a.Score.Value != expected {
		t.Fatalf("expected score %d, got %d", expected, a.Score.Value)
	}
	if a.Score.Level != RiskLow {
		t.Fatalf("expected level low, got %s", a.Score.Level)
	}
}

func TestAnalyzeSkillDescriptor_CappedAt100(t *testing.T) {
	// Create a skill with many high-risk factors
	sd := &contract.SkillDescriptor{
		Name:          "mega-risk",
		EgressDomains: []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"},
		RequiredBins:  []string{"bash", "python", "ssh", "nc"},
		RequiredEnv:   []string{"AWS_SECRET_ACCESS_KEY", "DB_PASSWORD"},
	}
	a := AnalyzeSkillDescriptor(sd, true)

	if a.Score.Value > 100 {
		t.Fatalf("score should be capped at 100, got %d", a.Score.Value)
	}
}

func TestAnalyzeSkillDescriptor_ManyDomainBonus(t *testing.T) {
	domains := []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"}
	sd := &contract.SkillDescriptor{
		Name:          "many-domains",
		EgressDomains: domains,
	}
	a := AnalyzeSkillDescriptor(sd, false)

	// 6 unknown domains * 10 = 60, plus bonus 15 = 75
	if a.Score.Value != 75 {
		t.Fatalf("expected score 75, got %d", a.Score.Value)
	}
}

func TestClassifyScore(t *testing.T) {
	tests := []struct {
		score int
		level RiskLevel
	}{
		{0, RiskNone},
		{1, RiskLow},
		{25, RiskLow},
		{26, RiskMedium},
		{50, RiskMedium},
		{51, RiskHigh},
		{75, RiskHigh},
		{76, RiskCritical},
		{100, RiskCritical},
	}
	for _, tt := range tests {
		got := classifyScore(tt.score)
		if got != tt.level {
			t.Errorf("classifyScore(%d) = %s, want %s", tt.score, got, tt.level)
		}
	}
}

func TestGenerateRecommendations(t *testing.T) {
	factors := []RiskFactor{
		{Category: "binary", Points: 15},
		{Category: "env", Points: 10},
		{Category: "egress", Points: 10},
	}
	recs := generateRecommendations(factors, true)

	if len(recs) < 3 {
		t.Fatalf("expected at least 3 recommendations, got %d", len(recs))
	}
}
