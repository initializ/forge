package builtins

import (
	"context"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

const (
	// MaxOutputLines is the maximum number of lines returned by tool output.
	MaxOutputLines = 2000
	// MaxOutputBytes is the maximum size of tool output in bytes.
	MaxOutputBytes = 50 * 1024

	// Relaxed limits apply when the context carries tools.WithRelaxedLimits
	// (compression enabled): a destructive tool-side cut would otherwise
	// destroy data before the compression hook can shrink it losslessly.
	// 16x mirrors the agent loop's pre-hook safety ceiling multiplier; the
	// loop still bounds everything downstream.
	RelaxedMaxOutputLines = MaxOutputLines * 16
	RelaxedMaxOutputBytes = MaxOutputBytes * 16
)

// TruncateOutput truncates a string to MaxOutputLines and MaxOutputBytes,
// appending a truncation notice if the output was trimmed.
func TruncateOutput(s string) string {
	return truncateOutput(s, MaxOutputLines, MaxOutputBytes)
}

// TruncateOutputCtx is TruncateOutput with context-aware limits: relaxed
// (16x) when compression is enabled, standard otherwise.
func TruncateOutputCtx(ctx context.Context, s string) string {
	if tools.RelaxedLimits(ctx) {
		return truncateOutput(s, RelaxedMaxOutputLines, RelaxedMaxOutputBytes)
	}
	return truncateOutput(s, MaxOutputLines, MaxOutputBytes)
}

func truncateOutput(s string, maxLines, maxBytes int) string {
	if len(s) == 0 {
		return s
	}

	truncated := false

	// Truncate by byte size first.
	if len(s) > maxBytes {
		s = s[:maxBytes]
		truncated = true
	}

	// Truncate by line count.
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
		s = strings.Join(lines, "")
	}

	if truncated {
		s = strings.TrimRight(s, "\n") + "\n\n... (output truncated)"
	}
	return s
}
