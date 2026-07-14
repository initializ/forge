package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/initializ/forge/forge-core/credentials"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/tools"
)

// webFetchTool fetches a single URL and returns its main content as clean,
// LLM-readable text/markdown — the "read this page" tool that web_search
// (results, not contents) and http_request (raw response) don't provide (#266).
//
// It reuses http_request's egress plumbing (the context egress transport +
// safe-redirect policy + optional JIT credential injector) rather than opening
// a second network path, so it honors the same allowlist / SSRF protections.
// Read-only GET; anything mutating stays on http_request.
type webFetchTool struct {
	credInjector *credentials.Injector
}

// WithCredentialInjector attaches an R9 JIT-credential injector, mirroring
// http_request so a domain's operator-declared credentials also apply when the
// agent reads a page there. nil-safe.
func (t *webFetchTool) WithCredentialInjector(inj *credentials.Injector) *webFetchTool {
	t.credInjector = inj
	return t
}

const (
	// webFetchByteLimit caps the raw body read BEFORE extraction, so a huge
	// page can't blow up memory. Scales with compression (RelaxedLimits) like
	// http_request.
	webFetchByteLimit        = 2 << 20 // 2 MiB
	webFetchByteLimitRelaxed = 8 << 20 // 8 MiB

	// webFetchDefaultMaxChars caps the CLEANED content returned to the model
	// when the caller doesn't specify max_chars. ~12-15k tokens of readable
	// text — enough for a doc page, bounded against context blowup.
	webFetchDefaultMaxChars = 50000

	webFetchTimeout      = 30 * time.Second
	webFetchMaxRedirects = 5
)

type webFetchInput struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL and return its main content as clean, readable text/markdown " +
		"(strips navigation, scripts, and styling). Read-only GET — use it to read a " +
		"specific page, doc, spec, or changelog. For raw response bytes/headers or a " +
		"non-GET method, use http_request instead; for finding pages, use web_search."
}
func (t *webFetchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *webFetchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "The URL to fetch (http/https, must be on the egress allowlist)"},
			"max_chars": {"type": "integer", "description": "Maximum characters of cleaned content to return (default 50000). Content over the cap is truncated with a marker."}
		},
		"required": ["url"]
	}`)
}

func (t *webFetchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input webFetchInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	if strings.TrimSpace(input.URL) == "" {
		return "", fmt.Errorf("url is required")
	}
	maxChars := input.MaxChars
	if maxChars <= 0 {
		maxChars = webFetchDefaultMaxChars
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	// A UA + Accept nudge servers toward returning HTML/text rather than a
	// bot-block or an API/JSON variant.
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,text/markdown;q=0.9,*/*;q=0.5")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "forge-web-fetch/1.0")
	}

	// R9 JIT credentials, same contract as http_request: operator-declared
	// creds override any LLM-supplied header. nil injector → no-op.
	if t.credInjector != nil {
		handle, herr := t.credInjector.Materialize(ctx, "web_fetch", "", args)
		if herr != nil {
			return "", fmt.Errorf("web_fetch: minting JIT credentials: %w", herr)
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
		Timeout:       webFetchTimeout,
		CheckRedirect: security.SafeRedirectPolicy(webFetchMaxRedirects),
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", input.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	kind, ok := classifyContentType(contentType)
	if !ok {
		// Don't stream a binary blob (image / pdf / octet-stream / video) back
		// into the context — return a clear, small message instead.
		return "", fmt.Errorf("web_fetch does not read non-textual content (Content-Type %q); use http_request for binary responses", firstToken(contentType))
	}

	limit := int64(webFetchByteLimit)
	if tools.RelaxedLimits(ctx) {
		limit = webFetchByteLimitRelaxed
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	var content string
	if kind == contentHTML {
		content = extractReadableText(string(raw))
	} else {
		content = strings.TrimSpace(string(raw))
	}

	truncated := false
	if len([]rune(content)) > maxChars {
		content = string([]rune(content)[:maxChars]) + "\n\n[content truncated at max_chars]"
		truncated = true
	}

	finalURL := input.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	result := map[string]any{
		"url":          finalURL,
		"content_type": firstToken(contentType),
		"status":       resp.StatusCode,
		"content":      content,
	}
	if truncated {
		result["truncated"] = true
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

// contentKind distinguishes HTML (needs extraction) from already-textual
// payloads (returned as-is).
type contentKind int

const (
	contentHTML contentKind = iota
	contentText
)

// classifyContentType returns whether the Content-Type is textual (and if so,
// whether it needs HTML extraction). An EMPTY content type is treated as HTML
// — servers commonly omit it for pages, and the extractor is safe on plain
// text too. Binary types (image/pdf/octet-stream/video/audio/font) return ok=false.
func classifyContentType(ct string) (contentKind, bool) {
	mt := strings.ToLower(firstToken(ct))
	switch {
	case mt == "":
		return contentHTML, true
	case mt == "text/html", mt == "application/xhtml+xml":
		return contentHTML, true
	case strings.HasPrefix(mt, "text/"):
		return contentText, true
	case mt == "application/json",
		mt == "application/xml",
		strings.HasSuffix(mt, "+xml"),
		strings.HasSuffix(mt, "+json"):
		return contentText, true
	default:
		return contentText, false
	}
}

// firstToken returns the media type without parameters ("text/html; charset=..."
// → "text/html"), trimmed.
func firstToken(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// skipTags are element subtrees dropped entirely during extraction — script /
// style / chrome. Their text never reaches the output.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "head": true,
	"svg": true, "template": true, "iframe": true, "nav": true,
	"footer": true, "aside": true, "form": true, "button": true,
	"select": true, "option": true,
}

// blockTags emit a newline around their content so structure survives.
var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true,
	"header": true, "ul": true, "ol": true, "table": true, "tr": true,
	"blockquote": true, "pre": true, "hr": true, "dl": true, "dd": true,
	"dt": true, "figure": true, "figcaption": true,
}

// extractReadableText parses HTML and returns clean, structured text: headings
// as markdown `#`, list items as `- `, links as `text (href)`, block elements
// separated by blank lines. script/style/nav chrome is dropped. Malformed HTML
// still parses (net/html is lenient); a hard parse failure falls back to the
// raw string so the caller always gets *something*.
func extractReadableText(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return strings.TrimSpace(htmlContent)
	}

	var b strings.Builder
	if title := findTitle(doc); title != "" {
		b.WriteString("# " + title + "\n\n")
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(collapseInlineWS(n.Data))
			return
		case html.ElementNode:
			if skipTags[n.Data] {
				return
			}
			switch {
			case isHeading(n.Data):
				b.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			case n.Data == "li":
				b.WriteString("\n- ")
			case n.Data == "br":
				b.WriteString("\n")
			case blockTags[n.Data]:
				b.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			if n.Data == "a" {
				if href := attr(n, "href"); isHTTPURL(href) {
					b.WriteString(" (" + href + ")")
				}
			}
			if isHeading(n.Data) || n.Data == "li" || blockTags[n.Data] {
				b.WriteString("\n")
			}
		}
	}
	walk(doc)
	return tidyText(b.String())
}

func isHeading(tag string) bool {
	return len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6'
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// findTitle returns the first <title> text, or "".
func findTitle(doc *html.Node) string {
	var title string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "title" && n.FirstChild != nil {
			title = strings.TrimSpace(n.FirstChild.Data)
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(doc)
	return title
}

// collapseInlineWS collapses runs of any whitespace (incl. incidental HTML
// source newlines/indentation) within a text node to a single space.
func collapseInlineWS(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// tidyText normalizes the assembled output: trim trailing spaces per line,
// collapse 3+ blank lines to one, and trim the whole.
func tidyText(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	var out []string
	blanks := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
