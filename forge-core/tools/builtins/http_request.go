// Package builtins provides built-in tools available to all agents.
package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/credentials"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/tools"
)

type httpRequestTool struct {
	// credInjector, when non-nil, mints JIT credentials per Execute
	// call and merges the resulting headers into the outbound HTTP
	// request. Governance R9 — the HTTP/API half of least-privilege
	// scoping (the subprocess env half lives in cli_execute). Nil
	// injector → no-op, preserving pre-R9 behavior.
	credInjector *credentials.Injector
}

// WithCredentialInjector attaches an R9 JIT-credential injector.
// Called by the runner at startup after resolving
// ForgeConfig.Credentials. nil-safe: passing nil is equivalent to
// not calling this.
func (t *httpRequestTool) WithCredentialInjector(inj *credentials.Injector) *httpRequestTool {
	t.credInjector = inj
	return t
}

const (
	// httpBodyLimitBytes caps a response body. With compression enabled
	// (tools.RelaxedLimits) the cap scales to the same 4MB absolute the
	// MCP adapter and the loop's safety ceiling use, so a big list-API
	// response reaches the compression layer instead of being cut
	// mid-JSON inside the tool.
	httpBodyLimitBytes        = 1 << 20 // 1 MiB
	httpBodyLimitBytesRelaxed = 4 << 20 // 4 MiB
)

type httpRequestInput struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

func (t *httpRequestTool) Name() string             { return "http_request" }
func (t *httpRequestTool) Description() string      { return "Make HTTP requests (GET, POST, PUT, DELETE)" }
func (t *httpRequestTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *httpRequestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"method": {"type": "string", "enum": ["GET", "POST", "PUT", "DELETE"], "description": "HTTP method"},
			"url": {"type": "string", "description": "URL to send the request to"},
			"headers": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Request headers"},
			"body": {"type": "string", "description": "Request body"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["method", "url"]
	}`)
}

func (t *httpRequestTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input httpRequestInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	timeout := time.Duration(input.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	var bodyReader io.Reader
	if input.Body != "" {
		bodyReader = strings.NewReader(input.Body)
	}

	req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	for k, v := range input.Headers {
		req.Header.Set(k, v)
	}

	// R9 JIT credentials: if the runner wired a credentials.Injector,
	// mint fresh scoped-down creds now, merge them into request
	// headers, and defer revocation. Nil injector → no-op.
	// JIT headers OVERRIDE any same-named header the LLM specified
	// via input.Headers — the operator's declared credential spec
	// takes precedence over an LLM-authored Authorization header.
	if t.credInjector != nil {
		handle, err := t.credInjector.Materialize(ctx, "http_request", "", args)
		if err != nil {
			return "", fmt.Errorf("http_request: minting JIT credentials: %w", err)
		}
		if handle != nil {
			defer func() { _ = handle.Close(ctx) }()
			for k, v := range handle.Headers() {
				req.Header.Set(k, v)
			}
		}
	}

	client := &http.Client{
		Transport:     security.EgressTransportFromContext(ctx),
		Timeout:       timeout,
		CheckRedirect: security.SafeRedirectPolicy(10),
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the cap so an over-limit body is detected and
	// reported via "truncated" instead of silently returning a partial
	// body (previously a bare LimitReader cut mid-JSON with no signal).
	limit := int64(httpBodyLimitBytes)
	if tools.RelaxedLimits(ctx) {
		limit = httpBodyLimitBytesRelaxed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	truncated := false
	if int64(len(body)) > limit {
		body = body[:limit]
		truncated = true
	}

	result := map[string]any{
		"status":      resp.StatusCode,
		"status_text": resp.Status,
		"body":        string(body),
	}
	if truncated {
		result["truncated"] = true
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}
