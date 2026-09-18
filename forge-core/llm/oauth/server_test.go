package oauth

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestHandleCallback_EscapesErrorDescription is a regression guard for the
// reflected-XSS finding: error_description is an untrusted OAuth query param
// written into the callback page, so it must be HTML-escaped.
func TestHandleCallback_EscapesErrorDescription(t *testing.T) {
	s := &CallbackServer{resultCh: make(chan CallbackResult, 1)}

	payload := `<script>alert(document.domain)</script>`
	target := "/callback?error=access_denied&error_description=" + url.QueryEscape(payload)
	req := httptest.NewRequest("GET", target, nil)
	rec := httptest.NewRecorder()

	s.handleCallback(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, payload) {
		t.Fatalf("error_description reflected UNESCAPED (XSS):\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected HTML-escaped description in page, got:\n%s", body)
	}

	// The raw (unescaped) value still reaches the terminal result channel — that
	// is not an HTML sink, so it should be verbatim.
	select {
	case res := <-s.resultCh:
		if !strings.Contains(res.Error, payload) {
			t.Errorf("result channel should carry the raw description, got %q", res.Error)
		}
	default:
		t.Error("expected a CallbackResult on the channel")
	}
}
