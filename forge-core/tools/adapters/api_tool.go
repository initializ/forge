package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/types"
)

// apiTool is one operation of an admitted OpenAPI API, registered as a
// namespaced "<server>__<op>" tool with the operation's typed input schema.
// Unlike the generic openapi_call adapter, each op is its own tool so a PDP can
// key value rules on it (reverse_fee.amount). Execute mirrors the http_request
// builtin: an authenticated request built from the op's method+path, sent
// through the egress-enforcing transport so the call is allowlist-gated.
type apiTool struct {
	server     string
	baseURL    string
	tokenEnv   string
	authScheme string // "" | "bearer" | "header" | "query" (#479)
	authName   string // header name / query key for the header|query schemes
	timeout    time.Duration
	op         types.APIOp
}

// API-tool auth schemes (APIAuth.Scheme). "" and "bearer" both mean the
// default Authorization: Bearer placement (handled by the switch default).
const (
	apiAuthHeader = "header"
	apiAuthQuery  = "query"
)

// apiPathParamRe matches {name} path template segments.
var apiPathParamRe = regexp.MustCompile(`\{([^}]+)\}`)

// appendQueryParam adds key=val to a URL's query string, preserving any
// existing params (e.g. the GET args already encoded onto rawURL) and escaping
// correctly. Used for the "query" auth scheme (#479).
func appendQueryParam(rawURL, key, val string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		return rawURL + sep + url.QueryEscape(key) + "=" + url.QueryEscape(val)
	}
	q := u.Query()
	q.Set(key, val)
	u.RawQuery = q.Encode()
	return u.String()
}

// NewAPITool builds a per-op API tool. auth may be nil (no outbound auth).
func NewAPITool(server, baseURL string, auth *types.APIAuth, op types.APIOp, timeout time.Duration) tools.Tool {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	t := &apiTool{server: server, baseURL: baseURL, timeout: timeout, op: op}
	if auth != nil {
		t.tokenEnv = auth.TokenEnv
		t.authScheme = strings.ToLower(strings.TrimSpace(auth.Scheme))
		t.authName = auth.Name
	}
	return t
}

// NamespacedSource opts this tool into the "__" namespace at registration
// (it legitimately uses the "<server>__<op>" form; it is not an MCP tool).
func (t *apiTool) NamespacedSource() {}

func (t *apiTool) Name() string { return t.server + "__" + t.op.Name }

func (t *apiTool) Description() string {
	if t.op.Description != "" {
		return t.op.Description
	}
	return fmt.Sprintf("%s %s", t.op.Method, t.op.Path)
}

func (t *apiTool) Category() tools.Category { return tools.CategoryAdapter }

func (t *apiTool) InputSchema() json.RawMessage {
	if len(t.op.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	b, err := json.Marshal(t.op.InputSchema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return b
}

func (t *apiTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	fields := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return "", fmt.Errorf("parsing input: %w", err)
		}
	}

	// Substitute {name} path params from args (dropping them from the rest).
	remaining := make(map[string]any, len(fields))
	for k, v := range fields {
		remaining[k] = v
	}
	path := apiPathParamRe.ReplaceAllStringFunc(t.op.Path, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := fields[name]; ok {
			delete(remaining, name)
			return url.PathEscape(fmt.Sprint(v))
		}
		return m // unfilled param — left as-is (will 404 upstream, surfaced verbatim)
	})
	fullURL := strings.TrimRight(t.baseURL, "/") + "/" + strings.TrimLeft(path, "/")

	method := strings.ToUpper(t.op.Method)
	if method == "" {
		method = http.MethodGet
	}

	// Placement heuristic: for read verbs the remaining args become query
	// params; for write verbs they become the JSON body.
	var bodyReader io.Reader
	jsonBody := false
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		if len(remaining) > 0 {
			q := url.Values{}
			for k, v := range remaining {
				q.Set(k, fmt.Sprint(v))
			}
			fullURL += "?" + q.Encode()
		}
	default:
		b, err := json.Marshal(remaining)
		if err != nil {
			return "", fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
		jsonBody = true
	}

	// Resolve the outbound token once; place it per the auth scheme (#479).
	// Query-param auth must land on the URL before the request is built.
	var authToken string
	if t.tokenEnv != "" {
		authToken = os.Getenv(t.tokenEnv)
	}
	if authToken != "" && t.authScheme == apiAuthQuery && t.authName != "" {
		fullURL = appendQueryParam(fullURL, t.authName, authToken)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	}
	// Header / bearer placement. Empty scheme defaults to Bearer, preserving
	// the pre-#479 behavior for every existing config.
	if authToken != "" {
		switch t.authScheme {
		case apiAuthHeader:
			if t.authName != "" {
				req.Header.Set(t.authName, authToken)
			}
		case apiAuthQuery:
			// already appended to the URL above
		default: // "" or "bearer"
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
	}

	// The egress-enforcing transport from context makes the call subject to the
	// agent's allowlist (the base_url host must be allowlisted; see APIDomains).
	client := &http.Client{
		Transport:     security.EgressTransportFromContext(ctx),
		Timeout:       t.timeout,
		CheckRedirect: security.SafeRedirectPolicy(10),
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	const bodyLimit = 1 << 20 // 1 MiB
	raw, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	truncated := false
	if len(raw) > bodyLimit {
		raw = raw[:bodyLimit]
		truncated = true
	}

	result := map[string]any{
		"status": resp.StatusCode,
		"body":   string(raw),
	}
	if truncated {
		result["truncated"] = true
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}
