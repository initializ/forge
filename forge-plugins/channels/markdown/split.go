package markdown

import "strings"

// SplitSummaryAndReport splits a long response into a summary and full report.
// The summary is the first paragraph (up to the first double newline), capped
// at 600 characters, with a note that the full report is attached as a file.
// If no paragraph boundary is found, falls back to a 500-char truncation at
// a sentence boundary.
func SplitSummaryAndReport(text string) (summary, report string) {
	report = text

	// Try to split at the first paragraph boundary
	if idx := strings.Index(text, "\n\n"); idx > 0 && idx <= 600 {
		summary = text[:idx]
	} else if idx > 600 {
		// First paragraph is too long, truncate at sentence boundary
		summary = truncateAtSentence(text, 500)
	} else {
		// No paragraph boundary found
		summary = truncateAtSentence(text, 500)
	}

	summary = strings.TrimSpace(summary) + "\n\n_Full report attached as file._"
	return summary, report
}

// truncateAtSentence truncates text to maxLen at a sentence boundary.
// Falls back to a word boundary, then hard truncation.
func truncateAtSentence(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}

	chunk := text[:maxLen]

	// Try sentence boundary (. ! ?)
	for i := len(chunk) - 1; i > maxLen/2; i-- {
		if chunk[i] == '.' || chunk[i] == '!' || chunk[i] == '?' {
			return chunk[:i+1]
		}
	}

	// Try word boundary
	if idx := strings.LastIndex(chunk, " "); idx > maxLen/2 {
		return chunk[:idx] + "..."
	}

	// Hard truncation
	return chunk + "..."
}
