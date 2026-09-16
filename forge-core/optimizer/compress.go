package optimizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/initializ/ctxzip"
	"github.com/initializ/ctxzip/ccr"

	"github.com/initializ/forge/forge-core/compress"
)

// contextExpandToolName is the tool the model calls to retrieve offloaded
// originals. It must match the tool the expansion server (phase 3) registers
// with Claude Code, and is the tool the compressor exempts from recompression.
const contextExpandToolName = "context_expand"

// minCompressChars is a cheap pre-gate: strings shorter than this are never
// worth a marker, so we skip ctxzip entirely. ctxzip's own MinTokens is the
// authoritative gate; this just avoids the call for trivial blocks.
const minCompressChars = 256

// CompressConfig configures the request compressor.
type CompressConfig struct {
	// StorePath is the bbolt file offloaded originals are written to. Because
	// bbolt is single-writer, this process must be the sole owner of the file —
	// the expansion server (phase 3) that reads it lives in this same process.
	StorePath string
	// TTL is how long offloaded originals stay retrievable. Default 30m.
	TTL time.Duration
	// MinTokens is ctxzip's per-block token floor. Default 50.
	MinTokens int
	// KeepPatterns are case-insensitive substrings compression must never drop.
	KeepPatterns []string
	// FreezePrefix is the number of leading messages never compressed (the
	// anchor turn). Default 1.
	FreezePrefix int
	// ProtectRecent is the number of trailing messages left verbatim, so the
	// model sees the freshest tool output uncompressed. Default 0 — compress
	// everything past the prefix. NOTE: a value > 0 causes a one-time cache
	// re-creation for each block as it ages out of the recent window; 0 is the
	// most cache-stable (every block is compressed from first appearance).
	ProtectRecent int
}

// Compressor rewrites outbound Anthropic /v1/messages request bodies, replacing
// bulky content in the live zone with retrievable markers.
//
// It works on RAW JSON, never round-tripping through provider-agnostic types,
// because those cannot express cache_control / thinking blocks / unknown fields
// — a round-trip would silently drop Claude Code's cache breakpoints.
//
// Cache strategy: compression is applied to EVERY eligible block (past the
// anchor prefix) deterministically — the same content + the same pinned query
// always yields the same marker bytes. That determinism is what keeps the
// prompt-cache prefix byte-stable across turns: a block compressed on the turn
// it first appears stays byte-identical on every resend, so Claude Code's
// cache_control breakpoints keep hitting and the cached prefix is simply
// smaller. (An earlier design compressed only the post-breakpoint "live zone"
// to avoid cache busts, but under Claude Code's aggressive caching that zone is
// nearly always empty — and position-dependent compression actually busts the
// cache as breakpoints slide. Deterministic whole-history compression is both
// safer and where the savings are.)
type Compressor struct {
	store         *ccr.BoltStore
	minTokens     int
	keep          []string
	freezePrefix  int
	protectRecent int
}

// NewCompressor opens the durable store and returns a Compressor.
func NewCompressor(cfg CompressConfig) (*Compressor, error) {
	if cfg.StorePath == "" {
		return nil, fmt.Errorf("optimizer: CompressConfig.StorePath is required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Minute
	}
	if cfg.MinTokens <= 0 {
		cfg.MinTokens = 50
	}
	if cfg.FreezePrefix < 0 {
		cfg.FreezePrefix = 0
	}
	if cfg.FreezePrefix == 0 {
		cfg.FreezePrefix = 1
	}
	if cfg.ProtectRecent < 0 {
		cfg.ProtectRecent = 0
	}
	if dir := filepath.Dir(cfg.StorePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("optimizer: creating store dir: %w", err)
		}
	}
	store, err := ccr.NewBoltStore(ccr.BoltConfig{Path: cfg.StorePath, TTL: cfg.TTL})
	if err != nil {
		return nil, fmt.Errorf("optimizer: opening store: %w", err)
	}
	return &Compressor{
		store:         store,
		minTokens:     cfg.MinTokens,
		keep:          cfg.KeepPatterns,
		freezePrefix:  cfg.FreezePrefix,
		protectRecent: cfg.ProtectRecent,
	}, nil
}

// Close releases the store.
func (c *Compressor) Close() error { return c.store.Close() }

// Store exposes the CCR store (used by the expansion server and tests).
func (c *Compressor) Store() ccr.Store { return c.store }

// CompressStats summarizes what one Transform did.
type CompressStats struct {
	SavedTokens  int `json:"saved_tokens"`
	TokensBefore int `json:"tokens_before"`
	TokensAfter  int `json:"tokens_after"`
	Blocks       int `json:"blocks"`
	Markers      int `json:"markers"`
}

// Transform rewrites an Anthropic /v1/messages request body. On any problem it
// returns the ORIGINAL body unchanged with a nil error — compression must never
// break a request (fail-open). The returned stats are zero when nothing was
// compressed.
func (c *Compressor) Transform(body []byte) ([]byte, CompressStats, error) {
	var st CompressStats

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, st, nil // not JSON we understand — pass through
	}
	msgsRaw, ok := root["messages"]
	if !ok {
		return body, st, nil
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil || len(msgs) == 0 {
		return body, st, nil
	}

	query := firstUserText(msgs)
	skipIDs := collectExpandIDs(msgs)

	// Compress every eligible block from the anchor prefix to the protected
	// recent tail. We do NOT gate on cache_control position: compressing
	// deterministically everywhere is both cache-stable (identical bytes every
	// turn) and where the savings live under Claude Code's caching. See the
	// Compressor doc for why the old breakpoint-gated approach compressed
	// nothing.
	start := c.freezePrefix
	if start < 0 {
		start = 0
	}
	end := len(msgs) - c.protectRecent
	if end > len(msgs) {
		end = len(msgs)
	}

	changed := false
	for i := start; i < end; i++ {
		nm, did := c.compressMessage(msgs[i], query, skipIDs, &st)
		if did {
			msgs[i] = nm
			changed = true
		}
	}

	// The directive is injected on EVERY compressed-enabled request (constant
	// text, appended after any cached system blocks) so the system prefix stays
	// byte-stable across turns even when a given turn compresses nothing.
	newSystem, sysChanged := injectDirective(root["system"])
	if sysChanged {
		root["system"] = newSystem
		changed = true
	}

	if !changed {
		return body, st, nil
	}

	newMsgs, err := json.Marshal(msgs)
	if err != nil {
		return body, CompressStats{}, nil
	}
	root["messages"] = newMsgs
	out, err := json.Marshal(root)
	if err != nil {
		return body, CompressStats{}, nil
	}
	return out, st, nil
}

// compressMessage compresses eligible blocks within one message. Returns the
// (possibly rewritten) message and whether anything changed.
func (c *Compressor) compressMessage(raw json.RawMessage, query string, skipIDs map[string]bool, st *CompressStats) (json.RawMessage, bool) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return raw, false
	}
	contentRaw, ok := msg["content"]
	if !ok {
		return raw, false
	}

	// content: array of blocks (the common case for tool results / tool use).
	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err == nil {
		changed := false
		for j, b := range blocks {
			nb, did := c.compressBlock(b, query, skipIDs, st)
			if did {
				blocks[j] = nb
				changed = true
			}
		}
		if !changed {
			return raw, false
		}
		nc, err := json.Marshal(blocks)
		if err != nil {
			return raw, false
		}
		msg["content"] = nc
		out, err := json.Marshal(msg)
		if err != nil {
			return raw, false
		}
		return out, true
	}

	// content: plain string.
	var s string
	if err := json.Unmarshal(contentRaw, &s); err == nil {
		ns, saved := c.compressText(roleOf(msg), s, "", query, st)
		if !saved {
			return raw, false
		}
		nc, _ := json.Marshal(ns)
		msg["content"] = nc
		out, err := json.Marshal(msg)
		if err != nil {
			return raw, false
		}
		return out, true
	}
	return raw, false
}

// compressBlock compresses one content block. Only text and tool_result blocks
// are eligible; tool_use / image / thinking blocks are structural and left
// verbatim. tool_result blocks whose tool_use_id belongs to context_expand are
// skipped — recompressing content the model just expanded recreates the marker
// it resolved (the expand/compress tail-chase).
func (c *Compressor) compressBlock(raw json.RawMessage, query string, skipIDs map[string]bool, st *CompressStats) (json.RawMessage, bool) {
	var blk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blk); err != nil {
		return raw, false
	}
	var typ string
	_ = json.Unmarshal(blk["type"], &typ)

	switch typ {
	case "text":
		var text string
		if json.Unmarshal(blk["text"], &text) != nil {
			return raw, false
		}
		nt, saved := c.compressText(ctxzip.RoleAssistant, text, "", query, st)
		if !saved {
			return raw, false
		}
		blk["text"], _ = json.Marshal(nt)

	case "tool_result":
		var tuid string
		_ = json.Unmarshal(blk["tool_use_id"], &tuid)
		if skipIDs[tuid] {
			return raw, false
		}
		cRaw, ok := blk["content"]
		if !ok {
			return raw, false
		}
		// tool_result content is a string or an array of (usually text) blocks.
		var s string
		if json.Unmarshal(cRaw, &s) == nil {
			ns, saved := c.compressText(ctxzip.RoleTool, s, "", query, st)
			if !saved {
				return raw, false
			}
			blk["content"], _ = json.Marshal(ns)
		} else {
			var sub []json.RawMessage
			if json.Unmarshal(cRaw, &sub) != nil {
				return raw, false
			}
			changed := false
			for k, sb := range sub {
				nb, did := c.compressBlock(sb, query, skipIDs, st)
				if did {
					sub[k] = nb
					changed = true
				}
			}
			if !changed {
				return raw, false
			}
			blk["content"], _ = json.Marshal(sub)
		}

	default:
		return raw, false
	}

	out, err := json.Marshal(blk)
	if err != nil {
		return raw, false
	}
	return out, true
}

// compressText runs one text through ctxzip as a single-message compression,
// mirroring forge's AfterToolExec hook. Returns the (possibly compressed) text
// and whether it was reduced. Determinism (pinned query, no freeze/protect on a
// single message) ensures the same input yields the same marker bytes every
// turn, which is what keeps Claude Code's re-sent history byte-stable.
func (c *Compressor) compressText(role, text, name, query string, st *CompressStats) (string, bool) {
	if len(text) < minCompressChars {
		return text, false
	}
	opts := ctxzip.DefaultOptions()
	opts.Store = c.store
	opts.FreezePrefix = 0
	opts.ProtectRecent = 0
	opts.Query = query
	opts.MustKeep = c.keep
	opts.MinTokens = c.minTokens
	opts.CompressRoles = map[string]bool{role: true}

	res, err := ctxzip.Compress([]ctxzip.Message{{Role: role, Content: text, Name: name}}, opts)
	if err != nil || res == nil || res.SavedTokens() <= 0 {
		return text, false
	}
	st.SavedTokens += res.SavedTokens()
	st.TokensBefore += res.TokensBefore
	st.TokensAfter += res.TokensAfter
	st.Blocks++
	for _, tr := range res.Transforms {
		st.Markers += len(tr.Markers)
	}
	return res.Messages[0].Content, true
}

// roleOf reads a message's role.
func roleOf(msg map[string]json.RawMessage) string {
	var role string
	_ = json.Unmarshal(msg["role"], &role)
	return role
}

// firstUserText returns the first user message's text — the pinned relevance
// query, stable for the whole session, which keeps compression deterministic
// across turns. Returns a single space when absent to suppress ctxzip's
// derive-from-recent fallback (which would vary per turn).
func firstUserText(msgs []json.RawMessage) string {
	for _, m := range msgs {
		var msg map[string]json.RawMessage
		if json.Unmarshal(m, &msg) != nil {
			continue
		}
		if roleOf(msg) != "user" {
			continue
		}
		if t := textOf(msg["content"]); t != "" {
			return t
		}
	}
	return " "
}

// textOf extracts concatenated text from a content field (string or block
// array), ignoring non-text blocks.
func textOf(contentRaw json.RawMessage) string {
	if len(contentRaw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(contentRaw, &s) == nil {
		return s
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(contentRaw, &blocks) != nil {
		return ""
	}
	var b bytes.Buffer
	for _, blk := range blocks {
		var typ string
		_ = json.Unmarshal(blk["type"], &typ)
		if typ != "text" {
			continue
		}
		var t string
		if json.Unmarshal(blk["text"], &t) == nil {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(t)
		}
	}
	return b.String()
}

// collectExpandIDs returns the set of tool_use ids whose tool is
// context_expand, so their tool_result blocks are never recompressed. The name
// lives only on the assistant's tool_use block, not on the later tool_result —
// which is exactly why the proxy must reconstruct this mapping.
func collectExpandIDs(msgs []json.RawMessage) map[string]bool {
	ids := map[string]bool{}
	for _, m := range msgs {
		if !bytes.Contains(m, []byte(contextExpandToolName)) {
			continue
		}
		var msg map[string]json.RawMessage
		if json.Unmarshal(m, &msg) != nil {
			continue
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(msg["content"], &blocks) != nil {
			continue
		}
		for _, blk := range blocks {
			var typ string
			_ = json.Unmarshal(blk["type"], &typ)
			if typ != "tool_use" {
				continue
			}
			var name string
			_ = json.Unmarshal(blk["name"], &name)
			if name != contextExpandToolName {
				continue
			}
			var id string
			if json.Unmarshal(blk["id"], &id) == nil && id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

// injectDirective appends compress.SystemDirective to the system field so the
// model knows markers exist and how to expand them. It appends AFTER any
// existing system blocks (which carry the cache breakpoints), so the cached
// prefix is never disturbed. Handles system as absent, a string, or a block
// array. Idempotent: if the directive is already present it makes no change.
func injectDirective(systemRaw json.RawMessage) (json.RawMessage, bool) {
	directive := compress.SystemDirective

	// Absent → set as a plain string.
	if len(systemRaw) == 0 {
		out, _ := json.Marshal(directive)
		return out, true
	}

	// String → concatenate.
	var s string
	if json.Unmarshal(systemRaw, &s) == nil {
		if bytes.Contains([]byte(s), []byte("Compressed context")) {
			return systemRaw, false
		}
		out, _ := json.Marshal(s + "\n\n" + directive)
		return out, true
	}

	// Array of blocks → append a text block.
	var blocks []json.RawMessage
	if json.Unmarshal(systemRaw, &blocks) == nil {
		for _, b := range blocks {
			if bytes.Contains(b, []byte("Compressed context")) {
				return systemRaw, false
			}
		}
		block, _ := json.Marshal(map[string]string{"type": "text", "text": directive})
		blocks = append(blocks, block)
		out, err := json.Marshal(blocks)
		if err != nil {
			return systemRaw, false
		}
		return out, true
	}

	return systemRaw, false
}
