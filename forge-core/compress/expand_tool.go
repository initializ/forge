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

func (t *expandTool) Name() string { return "context_expand" }

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

func (t *expandTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
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
// bare hash: "<<ctxzip:abc123 51_rows_offloaded>>", "ctxzip:abc123", or
// "hash=abc123" all normalize to "abc123".
func normalizeHash(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<<")
	s = strings.TrimPrefix(s, "ctxzip:")
	s = strings.TrimPrefix(s, "hash=")
	s = strings.TrimSuffix(s, ">>")
	// Keep only the leading token — markers carry a trailing note.
	if i := strings.IndexAny(s, " ,"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}
