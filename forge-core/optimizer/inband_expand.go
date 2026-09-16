package optimizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// In-band context_expand: instead of registering context_expand as an MCP tool
// (which requires `claude mcp add`), the optimizer injects the tool into the
// outbound request's `tools` array and intercepts the model's call in the
// RESPONSE. When the model calls context_expand, the proxy resolves the hash
// from its store, appends a synthetic tool_result, and re-issues the request
// upstream — looping until the model stops asking to expand — then returns the
// final answer to Claude Code, which never sees the tool. This makes retrieval
// zero-config (only ANTHROPIC_BASE_URL is needed).
//
// The cost: to inspect the response for a tool call, a marker-bearing turn must
// be buffered (we force stream:false upstream) and re-synthesized as SSE for
// the client. This trades streaming latency on those turns for zero setup —
// the same tradeoff Headroom's buffered-CCR flip makes.

// maxExpandRounds bounds the resolve→re-issue loop so a misbehaving model can't
// spin forever.
const maxExpandRounds = 4

// injectExpandTool appends the context_expand tool to the request's `tools`
// array (Anthropic uses the key "input_schema"). Idempotent, and deterministic
// so the tools bytes stay stable across turns (cache-safe). Appends AFTER any
// existing tools so a cache_control breakpoint on the client's last tool is not
// disturbed.
func injectExpandTool(body []byte) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	var tools []json.RawMessage
	if raw, ok := root["tools"]; ok && len(raw) > 0 {
		if json.Unmarshal(raw, &tools) != nil {
			return body
		}
	}
	needle := []byte(`"` + contextExpandToolName + `"`)
	for _, t := range tools {
		if bytes.Contains(t, needle) {
			return body // already present
		}
	}
	toolDef, err := json.Marshal(map[string]any{
		"name":         contextExpandToolName,
		"description":  expandToolDescription,
		"input_schema": json.RawMessage(expandToolInputSchema),
	})
	if err != nil {
		return body
	}
	tools = append(tools, toolDef)
	nt, err := json.Marshal(tools)
	if err != nil {
		return body
	}
	root["tools"] = nt
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// bodyHasCtxzipMarkers reports whether the request carries any compression
// marker — i.e. whether the model could meaningfully call context_expand. The
// "ctxzip:" hash token is not HTML-escaped by json.Marshal (only the "<<"/">>"
// are), so a substring check is reliable.
func bodyHasCtxzipMarkers(body []byte) bool {
	return bytes.Contains(body, []byte("ctxzip:"))
}

// wantsStream reports whether the client asked for a streaming response.
func wantsStream(body []byte) bool {
	var root struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &root)
	return root.Stream
}

// setStream sets the "stream" field on a request body.
func setStream(body []byte, v bool) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	b, _ := json.Marshal(v)
	root["stream"] = b
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

type expandCall struct {
	ID   string
	Hash string
}

// parseAssistantResponse extracts the assistant content array plus any
// context_expand tool calls, and reports whether the turn also contains OTHER
// (client-owned) tool calls.
func parseAssistantResponse(body []byte) (content json.RawMessage, expands []expandCall, otherTool bool) {
	var resp struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil, nil, false
	}
	content = resp.Content
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(resp.Content, &blocks) != nil {
		return content, nil, false
	}
	for _, b := range blocks {
		var typ string
		_ = json.Unmarshal(b["type"], &typ)
		if typ != "tool_use" {
			continue
		}
		var name, id string
		_ = json.Unmarshal(b["name"], &name)
		_ = json.Unmarshal(b["id"], &id)
		if name == contextExpandToolName {
			var input struct {
				Hash string `json:"hash"`
			}
			_ = json.Unmarshal(b["input"], &input)
			expands = append(expands, expandCall{ID: id, Hash: input.Hash})
		} else {
			otherTool = true
		}
	}
	return content, expands, otherTool
}

// appendExpandTurn appends the assistant turn (carrying the context_expand
// tool_use blocks) and a user turn with the resolved tool_result blocks, so the
// re-issued request continues the conversation with the expanded content.
func appendExpandTurn(body []byte, assistantContent json.RawMessage, toolResults []map[string]any) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	var msgs []json.RawMessage
	if json.Unmarshal(root["messages"], &msgs) != nil {
		return body
	}
	asst, _ := json.Marshal(map[string]any{"role": "assistant", "content": assistantContent})
	user, _ := json.Marshal(map[string]any{"role": "user", "content": toolResults})
	msgs = append(msgs, json.RawMessage(asst), json.RawMessage(user))
	nm, err := json.Marshal(msgs)
	if err != nil {
		return body
	}
	root["messages"] = nm
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// resolveExpandText looks up a marker hash, returning the original content (or a
// miss message) plus whether it hit.
func (s *Server) resolveExpandText(hash string) (string, bool) {
	h := normalizeHash(hash)
	if h == "" || s.compressor == nil {
		return "hash is required", false
	}
	entry, ok := s.compressor.Store().Get(h)
	if !ok {
		return "No stored content for hash " + h + " (expired or evicted). " +
			"Re-run the tool that produced the original output to regenerate it.", false
	}
	return string(entry.Original), true
}

// forwardOnce issues a single upstream request with the given body, copying the
// client's headers (auth, anthropic-version, beta). The caller reads and closes
// the response body.
func (s *Server) forwardOnce(ctx context.Context, r *http.Request, body []byte) (*http.Response, error) {
	target := *s.upstream
	target.Path = singleJoiningSlash(s.upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Del("Accept-Encoding")
	req.Header.Del("Content-Length")
	req.ContentLength = int64(len(body))
	req.Host = s.upstream.Host
	// Use the in-band client (no ResponseHeaderTimeout): this request is forced
	// non-streaming, so headers arrive only after the full generation, which can
	// exceed the main client's header deadline on long turns. See New().
	return s.inbandClient.Do(req)
}

// interceptAndForward runs the resolve→re-issue loop for a marker-bearing
// request, then delivers the final response to the client (as JSON, or
// re-synthesized SSE if the client asked to stream).
func (s *Server) interceptAndForward(w http.ResponseWriter, r *http.Request, cid, sessionID, client string, start time.Time, body []byte, comp CompressStats) {
	ccStream := wantsStream(body)
	// Force non-streaming upstream so we can inspect the whole turn.
	body = setStream(body, false)

	var total Usage
	rounds := 0
	var finalStatus int
	var finalHeader http.Header
	var finalBody []byte

	for {
		rounds++
		resp, err := s.forwardOnce(r.Context(), r, body)
		if err != nil {
			s.fail(w, r, cid, sessionID, start, http.StatusBadGateway, "forward to upstream (in-band)", err)
			return
		}
		rb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		u := parseJSONUsage(rb)
		total.Model = u.Model
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.CacheReadInputTokens += u.CacheReadInputTokens
		total.CacheCreationInputTokens += u.CacheCreationInputTokens

		finalStatus, finalHeader, finalBody = resp.StatusCode, resp.Header, rb

		if resp.StatusCode != http.StatusOK {
			break // upstream error — pass it through verbatim
		}
		content, expands, other := parseAssistantResponse(rb)
		if len(expands) == 0 {
			break // model didn't ask to expand — done
		}
		if other {
			// Mixed with a client-owned tool: we can't satisfy that half, so we
			// stop and return the turn. (Rare; the model usually calls
			// context_expand alone.) See README/limitations.
			s.log.Warn("context_expand mixed with client tool calls; returning turn without in-band expansion", "cid", cid)
			break
		}
		if rounds >= maxExpandRounds {
			s.log.Warn("context_expand hit max rounds; returning latest turn", "cid", cid, "rounds", rounds)
			break
		}

		results := make([]map[string]any, 0, len(expands))
		for _, e := range expands {
			text, ok := s.resolveExpandText(e.Hash)
			if s.stats != nil {
				s.stats.RecordExpansion(ok)
			}
			s.log.Info("context_expand (in-band)", "cid", cid, "hash", normalizeHash(e.Hash), "hit", ok, "bytes", len(text))
			results = append(results, map[string]any{
				"type":        "tool_result",
				"tool_use_id": e.ID,
				"content":     text,
			})
		}
		body = appendExpandTurn(body, content, results)
	}

	// Deliver the final turn to the client.
	if ccStream && finalStatus == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		writeMessageAsSSE(w, finalBody)
	} else {
		copyHeaders(w.Header(), finalHeader)
		w.Header().Del("Content-Length") // body written verbatim; let Go set it
		w.WriteHeader(finalStatus)
		_, _ = w.Write(finalBody)
	}

	total.Streaming = ccStream
	s.reporter.Report(r.Context(), Report{
		CorrelationID: cid,
		SessionID:     sessionID,
		Client:        client,
		Method:        r.Method,
		Path:          r.URL.Path,
		StatusCode:    finalStatus,
		Streaming:     ccStream,
		Usage:         total,
		Compression:   comp,
		Upstream:      s.upstream.String(),
		DurationMS:    time.Since(start).Milliseconds(),
	})
}

// writeMessageAsSSE re-synthesizes a non-streaming Anthropic Messages response
// into the SSE event sequence a streaming client expects. It handles text,
// tool_use, and thinking blocks; unknown block types are emitted as a
// start/stop with no delta (best effort). Enough for Claude Code to parse the
// turn after an in-band expansion.
func writeMessageAsSSE(w http.ResponseWriter, body []byte) {
	flusher, _ := w.(http.Flusher)
	emit := func(event string, data any) {
		raw, err := json.Marshal(data)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
		if flusher != nil {
			flusher.Flush()
		}
	}

	var msg map[string]json.RawMessage
	if json.Unmarshal(body, &msg) != nil {
		// Not parseable — surface as a single text turn so the client isn't
		// left hanging.
		emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"type": "message", "role": "assistant", "content": []any{},
		}})
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": string(body)}})
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"}})
		emit("message_stop", map[string]any{"type": "message_stop"})
		return
	}

	// message_start: the message envelope with an empty content array.
	startMsg := map[string]json.RawMessage{}
	for k, v := range msg {
		if k == "content" {
			continue
		}
		startMsg[k] = v
	}
	startMsg["content"] = json.RawMessage("[]")
	emit("message_start", map[string]any{"type": "message_start", "message": startMsg})

	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(msg["content"], &blocks)
	for i, blk := range blocks {
		var typ string
		_ = json.Unmarshal(blk["type"], &typ)
		switch typ {
		case "text":
			var text string
			_ = json.Unmarshal(blk["text"], &text)
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "text_delta", "text": text}})
		case "tool_use":
			var id, name string
			_ = json.Unmarshal(blk["id"], &id)
			_ = json.Unmarshal(blk["name"], &name)
			input := blk["input"]
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		case "thinking":
			var thinking, sig string
			_ = json.Unmarshal(blk["thinking"], &thinking)
			_ = json.Unmarshal(blk["signature"], &sig)
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "thinking", "thinking": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "thinking_delta", "thinking": thinking}})
			if sig != "" {
				emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
					"delta": map[string]any{"type": "signature_delta", "signature": sig}})
			}
		default:
			// Unknown block type: emit it whole as the start, no delta.
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": blk})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}

	// message_delta carries stop_reason + final usage; message_stop closes.
	delta := map[string]any{}
	if sr, ok := msg["stop_reason"]; ok {
		delta["stop_reason"] = sr
	}
	if ss, ok := msg["stop_sequence"]; ok {
		delta["stop_sequence"] = ss
	}
	md := map[string]any{"type": "message_delta", "delta": delta}
	if u, ok := msg["usage"]; ok {
		md["usage"] = u
	}
	emit("message_delta", md)
	emit("message_stop", map[string]any{"type": "message_stop"})
}
