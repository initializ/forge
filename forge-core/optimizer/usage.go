package optimizer

import (
	"bytes"
	"encoding/json"
)

// Usage is the token accounting extracted from an Anthropic response. Values
// are the provider's REAL billed counts (not tokenizer estimates), which is the
// key advantage of metering at the wire rather than inside the agent loop.
type Usage struct {
	Model                    string `json:"model,omitempty"`
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	Streaming                bool   `json:"streaming"`
}

// anthropicUsage mirrors the "usage" object Anthropic returns in both the
// non-streaming body and the streaming message_start / message_delta events.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// parseJSONUsage extracts usage from a complete (non-streaming) Messages
// response. Returns a zero Usage if the body isn't the expected shape.
func parseJSONUsage(body []byte) Usage {
	var resp struct {
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}
	}
	return Usage{
		Model:                    resp.Model,
		InputTokens:              resp.Usage.InputTokens,
		OutputTokens:             resp.Usage.OutputTokens,
		CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
	}
}

// sseUsageParser reconstructs Anthropic's token usage from a streamed SSE body
// fed in arbitrary byte chunks. Anthropic splits usage across two events:
//
//   - message_start carries model + input_tokens (+ cache read/creation) and
//     an initial output_tokens of 1;
//   - message_delta carries the FINAL cumulative output_tokens.
//
// So input/cache come from the start event and output is overwritten by the
// last delta. The parser is byte-chunk safe: SSE lines may be split across
// reads, so it buffers a residual tail until the next newline.
type sseUsageParser struct {
	residual []byte
	u        Usage
}

func newSSEUsageParser() *sseUsageParser { return &sseUsageParser{} }

// feed consumes a chunk of raw SSE bytes.
func (p *sseUsageParser) feed(chunk []byte) {
	p.residual = append(p.residual, chunk...)
	for {
		i := bytes.IndexByte(p.residual, '\n')
		if i < 0 {
			return
		}
		line := p.residual[:i]
		p.residual = p.residual[i+1:]
		p.line(bytes.TrimRight(line, "\r"))
	}
}

func (p *sseUsageParser) line(line []byte) {
	// Only "data:" lines carry JSON payloads; "event:" and blank lines are
	// framing.
	const prefix = "data:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	payload := bytes.TrimSpace(line[len(prefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}

	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return
	}

	switch ev.Type {
	case "message_start":
		p.u.Model = ev.Message.Model
		p.u.InputTokens = ev.Message.Usage.InputTokens
		p.u.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
		p.u.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
		p.u.OutputTokens = ev.Message.Usage.OutputTokens
	case "message_delta":
		// Final cumulative output token count.
		if ev.Usage.OutputTokens > 0 {
			p.u.OutputTokens = ev.Usage.OutputTokens
		}
	}
}

func (p *sseUsageParser) usage() Usage { return p.u }
