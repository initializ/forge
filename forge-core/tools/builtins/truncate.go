package builtins

import "strings"

const (
	// MaxOutputLines is the maximum number of lines returned by tool output.
	MaxOutputLines = 2000
	// MaxOutputBytes is the maximum size of tool output in bytes.
	MaxOutputBytes = 50 * 1024
)

// TruncateOutput truncates a string to MaxOutputLines and MaxOutputBytes,
// appending a truncation notice if the output was trimmed.
func TruncateOutput(s string) string {
	if len(s) == 0 {
		return s
	}

	truncated := false

	// Truncate by byte size first.
	if len(s) > MaxOutputBytes {
		s = s[:MaxOutputBytes]
		truncated = true
	}

	// Truncate by line count.
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > MaxOutputLines {
		lines = lines[:MaxOutputLines]
		truncated = true
		s = strings.Join(lines, "")
	}

	if truncated {
		s = strings.TrimRight(s, "\n") + "\n\n... (output truncated)"
	}
	return s
}
