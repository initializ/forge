package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/security"
)

func runWebFetch(t *testing.T, ctx context.Context, args map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := (&webFetchTool{}).Execute(ctx, raw)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if uerr := json.Unmarshal([]byte(out), &m); uerr != nil {
		t.Fatalf("result is not JSON: %v (%q)", uerr, out)
	}
	return m, nil
}

// TestWebFetch_ExtractsReadableText is the core AC: HTML in, clean text out —
// script/style/nav chrome dropped, headings/paragraph/link text kept.
func TestWebFetch_ExtractsReadableText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Doc Title</title>
			<style>.x{color:red}</style><script>alert('xss')</script></head>
			<body>
			<nav>Home About Contact</nav>
			<h1>Main Heading</h1>
			<p>The quick brown fox jumps over the lazy dog.</p>
			<ul><li>first item</li><li>second item</li></ul>
			<a href="https://example.com/spec">the spec</a>
			<footer>copyright 2026</footer>
			</body></html>`))
	}))
	defer srv.Close()

	m, err := runWebFetch(t, context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	content, _ := m["content"].(string)

	mustContain := []string{"Doc Title", "Main Heading", "The quick brown fox", "first item", "second item", "the spec", "https://example.com/spec"}
	for _, s := range mustContain {
		if !strings.Contains(content, s) {
			t.Errorf("cleaned content missing %q:\n%s", s, content)
		}
	}
	mustNotContain := []string{"alert(", "color:red", "Home About Contact", "copyright 2026"}
	for _, s := range mustNotContain {
		if strings.Contains(content, s) {
			t.Errorf("cleaned content should have stripped %q:\n%s", s, content)
		}
	}
	if m["content_type"] != "text/html" {
		t.Errorf("content_type = %v, want text/html", m["content_type"])
	}
}

// TestWebFetch_TruncatesAtMaxChars pins the size cap + marker.
func TestWebFetch_TruncatesAtMaxChars(t *testing.T) {
	body := "<html><body><p>" + strings.Repeat("word ", 5000) + "</p></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	m, err := runWebFetch(t, context.Background(), map[string]any{"url": srv.URL, "max_chars": 100})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if m["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", m["truncated"])
	}
	content, _ := m["content"].(string)
	if !strings.Contains(content, "[content truncated at max_chars]") {
		t.Errorf("missing truncation marker:\n%s", content)
	}
	// 100 chars + the marker line — well under the untruncated length.
	if len([]rune(content)) > 100+len([]rune("\n\n[content truncated at max_chars]")) {
		t.Errorf("content longer than max_chars + marker: %d runes", len([]rune(content)))
	}
}

// TestWebFetch_PlainTextPassthrough: non-HTML textual content returns as-is.
func TestWebFetch_PlainTextPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain body, not HTML"))
	}))
	defer srv.Close()

	m, err := runWebFetch(t, context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, _ := m["content"].(string); got != "plain body, not HTML" {
		t.Errorf("plain text content = %q", got)
	}
}

// TestWebFetch_RejectsBinaryContentType is the content-type guard: never stream
// a binary blob into the context.
func TestWebFetch_RejectsBinaryContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer srv.Close()

	_, err := runWebFetch(t, context.Background(), map[string]any{"url": srv.URL})
	if err == nil {
		t.Fatal("expected web_fetch to reject image/png")
	}
	if !strings.Contains(err.Error(), "non-textual") {
		t.Errorf("expected a non-textual content-type error, got %v", err)
	}
}

// denyTransport is an egress transport that refuses every request — stands in
// for a blocked (not-on-allowlist) domain.
type denyTransport struct{}

func (denyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("egress: domain not on allowlist")
}

// TestWebFetch_RoutesThroughEgressTransport proves web_fetch uses the context
// egress client (no second network path / bypass): a denying transport makes
// the fetch fail. Guards the "must respect the egress allowlist" AC.
func TestWebFetch_RoutesThroughEgressTransport(t *testing.T) {
	ctx := security.WithEgressClient(context.Background(), &http.Client{Transport: denyTransport{}})
	_, err := runWebFetch(t, ctx, map[string]any{"url": "https://blocked.example/page"})
	if err == nil {
		t.Fatal("expected the denying egress transport to fail the fetch")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("expected the egress denial to surface, got %v", err)
	}
}

// TestExtractReadableText_MalformedHTML: net/html is lenient, but a totally
// broken document must still yield text rather than an error.
func TestExtractReadableText_MalformedHTML(t *testing.T) {
	got := extractReadableText(`<p>hello <b>world</p></b> <div>tail`)
	for _, s := range []string{"hello", "world", "tail"} {
		if !strings.Contains(got, s) {
			t.Errorf("malformed HTML lost %q: %q", s, got)
		}
	}
}
