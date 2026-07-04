package compress

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/initializ/ctxzip"

	"github.com/initializ/forge/forge-core/tools"
)

// expandTool is the context_expand builtin. When compression drops content it
// leaves a "<<ctxzip:HASH n_rows_offloaded>>" marker behind; the model calls
// this tool with the hash to get the original back. The agent loop executes
// it like any other tool — no special retrieval machinery is needed.
type expandTool struct {
	rt *Runtime
}

// ExpandTool returns the context_expand tool backed by this Runtime's store.
// Register it conditionally (like memory_get) — only when compression is on.
func (r *Runtime) ExpandTool() tools.Tool {
	return &expandTool{rt: r}
}

type expandInput struct {
	Hash string `json:"hash"`
}

// expandToolName is referenced by the hook and client wrapper to exempt this
// tool's output from compression.
const expandToolName = "context_expand"

// SystemDirective is appended to the agent's system prompt whenever
// compression is enabled, so EVERY skill gets marker-awareness from the
// runtime — skill authors never need to document compression themselves.
// The text is constant, which keeps the system prompt byte-stable across
// turns (provider prompt caches stay warm).
const SystemDirective = `## Compressed context

Large tool outputs may be automatically compressed to fit your context.
Compressed sections are replaced inline by a marker like
<<ctxzip:HASH N_lines_offloaded>> — the note says how much was offloaded. The
visible remainder keeps errors, anomalies, and representative content, so for
many questions you will not need the offloaded part.

When you DO need offloaded data to answer precisely (exact counts, full
listings, a specific record you cannot see), call the ` + expandToolName + ` tool with the
marker's hash to retrieve the original content. If it reports the content
expired, re-run the tool that produced the output.`

func (t *expandTool) Name() string { return expandToolName }

func (t *expandTool) Description() string {
	return "Retrieve the original content behind a <<ctxzip:HASH ...>> compression marker. " +
		"Earlier tool results may contain such markers where bulky content was compressed away; " +
		"call this with the hash to see the full original data."
}

func (t *expandTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *expandTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"hash": {"type": "string", "description": "The hash from a <<ctxzip:HASH ...>> marker (the marker text itself is also accepted)"}
		},
		"required": ["hash"]
	}`)
}

func (t *expandTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input expandInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	hash := normalizeHash(input.Hash)
	if hash == "" {
		return "", fmt.Errorf("hash is required")
	}

	original, ok := ctxzip.Unzip(t.rt.store, hash)
	if !ok {
		// Models sometimes transcribe a marker hash imperfectly (truncated
		// hex). If the given value uniquely prefixes a hash this process
		// emitted, resolve and retry before declaring a miss.
		if full := t.rt.resolvePrefix(hash); full != "" {
			original, ok = ctxzip.Unzip(t.rt.store, full)
		}
	}
	t.rt.recordExpansion(ctx, hash, ok, len(original))
	if !ok {
		// A miss is not a dead end — the disk or the original command is the
		// source of truth. Say so instead of returning a bare error.
		return fmt.Sprintf(
			"No stored content for hash %s (expired or evicted). "+
				"Re-run the tool that produced the original output to regenerate it.",
			hash,
		), nil
	}
	return string(original), nil
}

// normalizeHash tolerates the model passing a whole marker instead of the
// bare hash: "<<ctxzip:abc123 51_rows_offloaded>>", "ctxzip:abc123",
// "hash=abc123", or "abc123:51" (count glued on, observed live) all
// normalize to "abc123".
func normalizeHash(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<<")
	s = strings.TrimPrefix(s, "ctxzip:")
	s = strings.TrimPrefix(s, "hash=")
	s = strings.TrimSuffix(s, ">>")
	// Keep only the leading token — markers carry a trailing note, and
	// models sometimes glue it on with a colon.
	if i := strings.IndexAny(s, " ,:"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}
