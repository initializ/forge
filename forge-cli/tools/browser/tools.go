package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	coretools "github.com/initializ/forge/forge-core/tools"
)

// Every tool result self-caps well below forge-core's loop-level truncation:
// blunt tail-chopping destroys a digest while still paying for its tokens.
const (
	defaultExtractChars = 16000
	maxExtractChars     = 100000
)

// --- browser_navigate ---

type navigateTool struct{ m *Manager }

func (t *navigateTool) Name() string { return "browser_navigate" }
func (t *navigateTool) Description() string {
	return "Load a web page in the managed browser and return its digest: title, URL, indexed interactive elements ([N] lines), and the start of the page text. Use the indices with browser_click/browser_fill. Only http/https URLs; navigation is subject to the agent's egress allowlist."
}
func (t *navigateTool) Category() coretools.Category { return coretools.CategoryBuiltin }
func (t *navigateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "Absolute http(s) URL to load"},
			"wait_ms": {"type": "integer", "description": "Extra settle time in milliseconds after load, for slow client-rendered pages (default 0, max 15000)"}
		},
		"required": ["url"]
	}`)
}

func (t *navigateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL    string `json:"url"`
		WaitMS int    `json:"wait_ms"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q: only http and https URLs can be browsed", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("url must be absolute (https://host/path)")
	}
	snap, err := t.m.Navigate(u.String(), in.WaitMS, 0)
	if err != nil {
		return "", browseError(err)
	}
	return buildDigest(snap), nil
}

// --- browser_state ---

type stateTool struct{ m *Manager }

func (t *stateTool) Name() string { return "browser_state" }
func (t *stateTool) Description() string {
	return "Re-read the current page and return a fresh digest with new element indices and generation. Use after the page changed on its own, to see more elements, or to scroll (scroll_pages down/up or scroll_to_index)."
}
func (t *stateTool) Category() coretools.Category { return coretools.CategoryBuiltin }
func (t *stateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"max_elements": {"type": "integer", "description": "How many interactive elements to list (default 100)"},
			"scroll_pages": {"type": "number", "description": "Scroll by this many viewport heights before reading (positive = down, negative = up)"},
			"scroll_to_index": {"type": "integer", "description": "Scroll the element with this index (from the previous digest) into view before reading"}
		}
	}`)
}

func (t *stateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		MaxElements   int     `json:"max_elements"`
		ScrollPages   float64 `json:"scroll_pages"`
		ScrollToIndex *int    `json:"scroll_to_index"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	scrollToIndex := -1
	if in.ScrollToIndex != nil {
		scrollToIndex = *in.ScrollToIndex
	}
	snap, err := t.m.Snapshot(in.MaxElements, scrollToIndex, in.ScrollPages)
	if err != nil {
		return "", browseError(err)
	}
	return buildDigest(snap), nil
}

// --- browser_click ---

type clickTool struct{ m *Manager }

func (t *clickTool) Name() string { return "browser_click" }
func (t *clickTool) Description() string {
	return "Click the interactive element with the given index from the latest digest, then return the resulting page digest. Requires the digest's generation number; if the page changed since, the error includes a fresh digest to act on."
}
func (t *clickTool) Category() coretools.Category { return coretools.CategoryBuiltin }
func (t *clickTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"index": {"type": "integer", "description": "Element index [N] from the latest digest"},
			"generation": {"type": "integer", "description": "Generation number from the latest digest"}
		},
		"required": ["index", "generation"]
	}`)
}

func (t *clickTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Index      int   `json:"index"`
		Generation int64 `json:"generation"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	snap, err := t.m.Click(in.Index, in.Generation, 0)
	if err != nil {
		if errors.Is(err, ErrStale) {
			return t.m.freshDigestForStale()
		}
		return "", browseError(err)
	}
	return fmt.Sprintf("Clicked [%d].\n\n%s", in.Index, buildDigest(snap)), nil
}

// --- browser_fill ---

type fillTool struct{ m *Manager }

func (t *fillTool) Name() string { return "browser_fill" }
func (t *fillTool) Description() string {
	return "Type text into the input/textarea/contenteditable with the given index (replaces existing content), or pick an option of a select by its label. Password and payment fields are refused unless the skill opted in. Set submit=true to press Enter afterwards. Returns the resulting page digest."
}
func (t *fillTool) Category() coretools.Category { return coretools.CategoryBuiltin }
func (t *fillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"index": {"type": "integer", "description": "Element index [N] from the latest digest"},
			"text": {"type": "string", "description": "Text to type, or the option label for a select"},
			"generation": {"type": "integer", "description": "Generation number from the latest digest"},
			"submit": {"type": "boolean", "description": "Press Enter after filling (default false)"}
		},
		"required": ["index", "text", "generation"]
	}`)
}

func (t *fillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Index      int    `json:"index"`
		Text       string `json:"text"`
		Generation int64  `json:"generation"`
		Submit     bool   `json:"submit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	snap, err := t.m.Fill(in.Index, in.Text, in.Generation, in.Submit, t.m.cfg.AllowSensitiveFill, 0)
	if err != nil {
		if errors.Is(err, ErrStale) {
			return t.m.freshDigestForStale()
		}
		return "", browseError(err)
	}
	return fmt.Sprintf("Filled [%d].\n\n%s", in.Index, buildDigest(snap)), nil
}

// --- browser_extract ---

type extractTool struct{ m *Manager }

func (t *extractTool) Name() string { return "browser_extract" }
func (t *extractTool) Description() string {
	return "Extract content from the current page: mode=text (readable markdown, default), mode=links (deduplicated [text](url) lines), or mode=html (outerHTML of a CSS selector — escape hatch, prefer text). Long content is paginated: pass offset from the previous call's header to continue."
}
func (t *extractTool) Category() coretools.Category { return coretools.CategoryBuiltin }
func (t *extractTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"mode": {"type": "string", "enum": ["text", "links", "html"], "description": "What to extract (default text)"},
			"selector": {"type": "string", "description": "CSS selector to scope the extraction (required for html mode)"},
			"max_chars": {"type": "integer", "description": "Max characters to return (default 16000)"},
			"offset": {"type": "integer", "description": "Character offset to continue from (default 0)"}
		}
	}`)
}

func (t *extractTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Mode     string `json:"mode"`
		Selector string `json:"selector"`
		MaxChars int    `json:"max_chars"`
		Offset   int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	switch in.Mode {
	case "":
		in.Mode = "text"
	case "text", "links", "html":
	default:
		return "", fmt.Errorf("invalid mode %q: use text, links, or html", in.Mode)
	}
	if in.Mode == "html" && strings.TrimSpace(in.Selector) == "" {
		return "", errors.New("html mode requires a selector — full-page HTML is never returned (use mode=text)")
	}
	if in.MaxChars <= 0 {
		in.MaxChars = defaultExtractChars
	}
	if in.MaxChars > maxExtractChars {
		in.MaxChars = maxExtractChars
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	content, pageURL, err := t.m.Extract(in.Mode, in.Selector)
	if err != nil {
		return "", browseError(err)
	}

	total := len(content)
	if in.Offset >= total && total > 0 {
		return "", fmt.Errorf("offset %d is beyond the content length (%d chars)", in.Offset, total)
	}
	end := in.Offset + in.MaxChars
	if end > total {
		end = total
	}
	// Snap to rune boundaries so pagination never splits a UTF-8 sequence.
	start := in.Offset
	for start > 0 && start < total && !utf8Start(content[start]) {
		start--
	}
	for end > start && end < total && !utf8Start(content[end]) {
		end--
	}
	chunk := content[start:end]

	var header string
	if total > len(chunk) {
		header = fmt.Sprintf("URL: %s\nchars %d-%d of %d", pageURL, start, end, total)
		if end < total {
			header += fmt.Sprintf(" (pass offset=%d for more)", end)
		}
	} else {
		header = fmt.Sprintf("URL: %s\n%d chars", pageURL, total)
	}
	return header + "\n\n" + chunk, nil
}

// --- browser_screenshot ---

type screenshotTool struct{ m *Manager }

func (t *screenshotTool) Name() string { return "browser_screenshot" }
func (t *screenshotTool) Description() string {
	return "Capture a PNG screenshot of the current page for the user (uploaded as a file attachment; you will not see the image). Use only when the user asked for a visual, not to read pages — use digests and browser_extract for that."
}
func (t *screenshotTool) Category() coretools.Category { return coretools.CategoryBuiltin }
func (t *screenshotTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"full_page": {"type": "boolean", "description": "Capture the entire page instead of the viewport (default false)"},
			"filename": {"type": "string", "description": "Output file name (default screenshot-<n>.png)"}
		}
	}`)
}

func (t *screenshotTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		FullPage bool   `json:"full_page"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	png, err := t.m.Screenshot(in.FullPage)
	if err != nil {
		return "", browseError(err)
	}

	dir := coreruntime.FilesDirFromContext(ctx)
	if dir == "" {
		dir = filepath.Join(t.m.cfg.WorkDir, ".forge-browser", "shots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating screenshot dir: %w", err)
	}

	name := sanitizeFilename(in.Filename)
	if name == "" {
		name = fmt.Sprintf("screenshot-%d.png", t.m.nextShot())
	}
	if !strings.HasSuffix(strings.ToLower(name), ".png") {
		name += ".png"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", fmt.Errorf("writing screenshot: %w", err)
	}

	// No base64 in the result: the loop reads bytes from path and attaches
	// them as a channel artifact; the LLM only sees this confirmation.
	out, _ := json.Marshal(map[string]any{
		"filename":   name,
		"mime_type":  "image/png",
		"path":       path,
		"size_bytes": len(png),
	})
	return string(out), nil
}

// sanitizeFilename keeps a user/LLM-supplied name safely inside the target dir.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
}

// browseError normalizes chromedp/proxy errors into something the LLM can act
// on (in particular: egress denials should read as policy, not as flakiness).
func browseError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "ERR_TUNNEL_CONNECTION_FAILED") || strings.Contains(msg, "ERR_PROXY_CONNECTION_FAILED") {
		return errors.New("navigation blocked by the egress policy: the domain is not in this agent's allowlist")
	}
	if strings.Contains(msg, "ERR_NAME_NOT_RESOLVED") {
		return errors.New("navigation failed: domain could not be resolved (or was blocked by the egress policy)")
	}
	return err
}

// freshDigestForStale returns a stale-index recovery message with a brand-new
// snapshot, so the LLM can retry with valid indices in the same turn.
func (m *Manager) freshDigestForStale() (string, error) {
	fresh, err := m.Snapshot(0, -1, 0)
	if err != nil {
		return "", fmt.Errorf("%w (and re-snapshot failed: %v)", ErrStale, err)
	}
	return staleRecovery(fresh), nil
}
