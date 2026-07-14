package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/credentials"
	_ "github.com/initializ/forge/forge-core/credentials/static" // register the "static" provider
	"github.com/initializ/forge/forge-core/security"
)

// egressCtx installs a permissive egress client (DefaultTransport) so tests can
// reach the localhost httptest server. web_fetch REFUSES when no egress client
// is present (fail-closed), so every real-fetch test must install one.
func egressCtx() context.Context {
	return security.WithEgressClient(context.Background(), &http.Client{Transport: http.DefaultTransport})
}

func runWebFetch(t *testing.T, ctx context.Context, args map[string]any) (map[string]any, error) {
	t.Helper()
	return runWebFetchTool(t, &webFetchTool{}, ctx, args)
}

func runWebFetchTool(t *testing.T, tool *webFetchTool, ctx context.Context, args map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := tool.Execute(ctx, raw)
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

	m, err := runWebFetch(t, egressCtx(), map[string]any{"url": srv.URL})
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

	m, err := runWebFetch(t, egressCtx(), map[string]any{"url": srv.URL, "max_chars": 100})
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

	m, err := runWebFetch(t, egressCtx(), map[string]any{"url": srv.URL})
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

	_, err := runWebFetch(t, egressCtx(), map[string]any{"url": srv.URL})
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

// TestExtractReadableText_PreservesPreformatted is the #266-review fix: <pre>/
// <code> content keeps its newlines + indentation instead of collapsing to one
// run of tokens — the tool's headline use case is reading docs, where code
// blocks live.
func TestExtractReadableText_PreservesPreformatted(t *testing.T) {
	got := extractReadableText("<html><body><pre>func main() {\n    fmt.Println(\"hi\")\n}</pre></body></html>")
	if !strings.Contains(got, "func main() {\n") {
		t.Errorf("pre newlines collapsed:\n%q", got)
	}
	if !strings.Contains(got, "\n    fmt.Println") {
		t.Errorf("pre indentation collapsed:\n%q", got)
	}
	// A normal paragraph outside <pre> still collapses incidental whitespace.
	para := extractReadableText("<p>one   two\n\tthree</p>")
	if strings.Contains(para, "   ") {
		t.Errorf("non-pre whitespace should collapse: %q", para)
	}
}

// TestWebFetch_RefusesWithoutEgressClient pins the fail-CLOSED behavior: with no
// egress client in context, web_fetch refuses rather than falling back to
// http.DefaultTransport (which would bypass the allowlist / SSRF protections).
func TestWebFetch_RefusesWithoutEgressClient(t *testing.T) {
	_, err := (&webFetchTool{}).Execute(context.Background(), []byte(`{"url":"https://example.com"}`))
	if err == nil || !strings.Contains(err.Error(), "egress enforcement") {
		t.Fatalf("expected a refusal without an egress client, got %v", err)
	}
}

// TestClassifyContentType covers the guard's branches, including the documented
// empty-Content-Type → HTML behavior and the binary rejections.
func TestClassifyContentType(t *testing.T) {
	cases := []struct {
		ct       string
		wantKind contentKind
		wantOK   bool
	}{
		{"", contentHTML, true}, // documented: empty CT treated as HTML
		{"text/html; charset=utf-8", contentHTML, true},
		{"application/xhtml+xml", contentHTML, true},
		{"text/plain", contentText, true},
		{"text/markdown", contentText, true},
		{"application/json", contentText, true},
		{"application/rss+xml", contentText, true},
		{"image/png", contentText, false},
		{"application/pdf", contentText, false},
		{"application/octet-stream", contentText, false},
	}
	for _, c := range cases {
		k, ok := classifyContentType(c.ct)
		if ok != c.wantOK || (ok && k != c.wantKind) {
			t.Errorf("classifyContentType(%q) = (%d,%v), want (%d,%v)", c.ct, k, ok, c.wantKind, c.wantOK)
		}
	}
}

// TestWebFetch_TranscodesCharset pins the charset fix: an ISO-8859-1 page
// (common for older spec/RFC pages) comes back as valid UTF-8, not mojibake.
func TestWebFetch_TranscodesCharset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		// "café latte" with é as the Latin-1 byte 0xE9.
		_, _ = w.Write([]byte("<html><body><p>caf\xe9 latte</p></body></html>"))
	}))
	defer srv.Close()

	m, err := runWebFetch(t, egressCtx(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if content, _ := m["content"].(string); !strings.Contains(content, "café latte") {
		t.Errorf("ISO-8859-1 not transcoded to UTF-8: %q", content)
	}
}

// TestWebFetch_StampsInjectedHeaders pins that the R9 JIT credential injector's
// materialized headers reach the outbound request (same plumbing as
// http_request).
func TestWebFetch_StampsInjectedHeaders(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Api-Token")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	inj, err := credentials.NewInjector(context.Background(), credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "web_fetch",
			Provider: "static",
			Spec:     json.RawMessage(`{"headers":{"X-Api-Token":"secret123"}}`),
		}}, nil)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	tool := (&webFetchTool{}).WithCredentialInjector(inj)
	if _, ferr := runWebFetchTool(t, tool, egressCtx(), map[string]any{"url": srv.URL}); ferr != nil {
		t.Fatalf("Execute: %v", ferr)
	}
	if h := <-got; h != "secret123" {
		t.Errorf("injected header not stamped on the request: got %q", h)
	}
}

// TestWebFetch_ByteCapBoundsRawRead pins the 2 MiB pre-extraction byte cap: a
// response far larger than the cap yields bounded content even when max_chars
// is effectively unlimited.
func TestWebFetch_ByteCapBoundsRawRead(t *testing.T) {
	big := strings.Repeat("A", 3<<20) // 3 MiB > webFetchByteLimit (2 MiB)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	m, err := runWebFetch(t, egressCtx(), map[string]any{"url": srv.URL, "max_chars": 100_000_000})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	content, _ := m["content"].(string)
	if len(content) > webFetchByteLimit {
		t.Errorf("content %d bytes exceeds the byte cap %d", len(content), webFetchByteLimit)
	}
}
