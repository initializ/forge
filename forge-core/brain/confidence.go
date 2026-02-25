package brain

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Confidence signal weights.
const (
	weightToolStructure = 0.35
	weightLength        = 0.20
	weightHedging       = 0.25
	weightRepetition    = 0.20
)

// hedgingPatterns are regex patterns indicating uncertainty.
var hedgingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bi'?m not sure\b`),
	regexp.MustCompile(`(?i)\bi think\b`),
	regexp.MustCompile(`(?i)\bpossibly\b`),
	regexp.MustCompile(`(?i)\bmaybe\b`),
	regexp.MustCompile(`(?i)\bperhaps\b`),
	regexp.MustCompile(`(?i)\bprobably\b`),
	regexp.MustCompile(`(?i)\bi don'?t know\b`),
	regexp.MustCompile(`(?i)\bnot certain\b`),
	regexp.MustCompile(`(?i)\bmight be\b`),
	regexp.MustCompile(`(?i)\bcould be\b`),
	regexp.MustCompile(`(?i)\bi believe\b`),
	regexp.MustCompile(`(?i)\bunclear\b`),
}

// ScoreConfidence computes a heuristic confidence score [0.0, 1.0] for a brain response.
// It uses four signals: tool-call structure validity, response length,
// hedging language detection, and repetition detection.
func ScoreConfidence(result *chatResult, hasTools bool) float64 {
	toolScore := scoreToolStructure(result, hasTools)
	lengthScore := scoreLength(result.Content)
	hedgeScore := scoreHedging(result.Content)
	repScore := scoreRepetition(result.Content)

	return toolScore*weightToolStructure +
		lengthScore*weightLength +
		hedgeScore*weightHedging +
		repScore*weightRepetition
}

// scoreToolStructure checks if tool calls are well-formed when tools were requested.
func scoreToolStructure(result *chatResult, hasTools bool) float64 {
	if !hasTools {
		// No tools requested — full score if there's content
		if len(strings.TrimSpace(result.Content)) > 0 || len(result.ToolCalls) > 0 {
			return 1.0
		}
		return 0.5
	}

	// Tools were requested — check if tool calls are valid
	if len(result.ToolCalls) > 0 {
		valid := 0
		for _, tc := range result.ToolCalls {
			if tc.Function.Name != "" && json.Valid([]byte(tc.Function.Arguments)) {
				valid++
			}
		}
		return float64(valid) / float64(len(result.ToolCalls))
	}

	// No tool calls but had content — moderate score
	if len(strings.TrimSpace(result.Content)) > 0 {
		return 0.6
	}
	return 0.2
}

// scoreLength penalizes very short or very long responses.
func scoreLength(content string) float64 {
	words := len(strings.Fields(content))

	switch {
	case words == 0:
		return 0.0
	case words < 3:
		return 0.3
	case words < 5:
		return 0.6
	case words > 500:
		return 0.5
	case words > 300:
		return 0.7
	default:
		return 1.0
	}
}

// scoreHedging checks for hedging/uncertainty language.
func scoreHedging(content string) float64 {
	if content == "" {
		return 0.5
	}

	matches := 0
	for _, pat := range hedgingPatterns {
		if pat.MatchString(content) {
			matches++
		}
	}

	switch matches {
	case 0:
		return 1.0
	case 1:
		return 0.7
	case 2:
		return 0.5
	default:
		return 0.3
	}
}

// scoreRepetition detects repeated phrases or degenerate output.
func scoreRepetition(content string) float64 {
	if content == "" {
		return 0.5
	}

	sentences := splitSentences(content)
	if len(sentences) < 2 {
		return 1.0
	}

	// Check for duplicate sentences
	seen := make(map[string]int)
	for _, s := range sentences {
		normalized := strings.TrimSpace(strings.ToLower(s))
		if len(normalized) > 5 { // skip very short fragments
			seen[normalized]++
		}
	}

	duplicates := 0
	for _, count := range seen {
		if count > 1 {
			duplicates += count - 1
		}
	}

	ratio := float64(duplicates) / float64(len(sentences))
	switch {
	case ratio == 0:
		return 1.0
	case ratio < 0.2:
		return 0.7
	case ratio < 0.5:
		return 0.4
	default:
		return 0.1
	}
}

// splitSentences splits text into sentences on common delimiters.
func splitSentences(text string) []string {
	re := regexp.MustCompile(`[.!?\n]+`)
	parts := re.Split(text, -1)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
