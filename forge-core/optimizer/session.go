package optimizer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// SessionHeader lets a caller pin an explicit session id, overriding the
// derived one. Claude Code sends no session id on the wire, so this is the
// escape hatch for clients (or a wrapper) that can inject one.
const SessionHeader = "X-Forge-Session-Id"

// deriveSessionID returns a stable per-conversation id for a request. It
// prefers an explicit X-Forge-Session-Id header; otherwise it hashes the model
// plus the system prompt. Claude Code's system prompt (tools, CLAUDE.md, cwd,
// env) is fixed for a session and lives in the top-level `system` field, so the
// hash is stable across a conversation's turns — and, unlike a per-launch uuid,
// it re-derives to the SAME id when a session is resumed. Two simultaneous
// sessions with an identical system prompt collapse to one id; that is the
// documented limit of deriving identity from the wire (same as Headroom).
//
// Returns "" when neither a header nor a model/system is present (e.g. a
// non-Messages request), in which case stats bucket the request as "unknown".
func deriveSessionID(header http.Header, body []byte) string {
	if v := header.Get(SessionHeader); v != "" {
		return v
	}
	model, system := modelAndSystem(body)
	if model == "" && system == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(model + "\x00" + system))
	return "sess_" + hex.EncodeToString(sum[:])[:16]
}

// modelAndSystem extracts the model id and system-prompt text from a Messages
// request body. `system` may be a string or an array of text blocks.
func modelAndSystem(body []byte) (model, system string) {
	var root struct {
		Model  string          `json:"model"`
		System json.RawMessage `json:"system"`
	}
	if json.Unmarshal(body, &root) != nil {
		return "", ""
	}
	return root.Model, textOf(root.System)
}
