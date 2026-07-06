package builtins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/credentials"
	_ "github.com/initializ/forge/forge-core/credentials/static" // register the static provider
)

// captureSink records emitted audit events so tests can assert on
// them without wiring the full AuditLogger machinery.
type captureSink struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	name   string
	fields map[string]any
}

func (c *captureSink) Emit(_ context.Context, name string, fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, capturedEvent{name, fields})
}

// TestHTTPRequest_JITHeadersInjected is the acceptance test for the
// R9 HTTP/API path (@initializ-mk's #236 fix #3): a spec routed to
// http_request materializes headers and stamps them on the outbound
// request. Verifies Headers() surface is no longer dead code.
func TestHTTPRequest_JITHeadersInjected(t *testing.T) {
	// Server captures the headers it received.
	var gotAuth, gotXForge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXForge = r.Header.Get("X-Forge-Test")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	sink := &captureSink{}
	inj, err := credentials.NewInjector(
		context.Background(),
		credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "http_request",
			Provider: "static",
			Spec: json.RawMessage(`{
				"headers": {
					"Authorization": "Bearer jit-token-abc",
					"X-Forge-Test": "yes"
				},
				"ttl": "5m"
			}`),
		}},
		sink,
	)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	tool := (&httpRequestTool{}).WithCredentialInjector(inj)
	args, _ := json.Marshal(httpRequestInput{
		Method: "GET",
		URL:    srv.URL + "/api",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAuth != "Bearer jit-token-abc" {
		t.Errorf("Authorization: got %q want Bearer jit-token-abc", gotAuth)
	}
	if gotXForge != "yes" {
		t.Errorf("X-Forge-Test: got %q want yes", gotXForge)
	}

	// Audit events fired: issued + revoked.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) < 2 {
		t.Fatalf("expected 2+ events, got %d", len(sink.events))
	}
	if sink.events[0].name != "credential_issued" {
		t.Errorf("first event: got %q want credential_issued", sink.events[0].name)
	}
	if sink.events[len(sink.events)-1].name != "credential_revoked" {
		t.Errorf("last event: got %q want credential_revoked", sink.events[len(sink.events)-1].name)
	}
}

// TestHTTPRequest_JITHeadersOverrideLLMHeaders proves the security
// property: an LLM-authored Authorization header is overridden by
// the JIT-provided one. Same shape as the env-override test on
// cli_execute — an LLM must not be able to bypass the operator's
// credential scoping by inlining a competing header.
func TestHTTPRequest_JITHeadersOverrideLLMHeaders(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	inj, _ := credentials.NewInjector(
		context.Background(),
		credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "http_request",
			Provider: "static",
			Spec:     json.RawMessage(`{"headers": {"Authorization": "Bearer jit-wins"}}`),
		}},
		nil,
	)

	tool := (&httpRequestTool{}).WithCredentialInjector(inj)
	args, _ := json.Marshal(httpRequestInput{
		Method:  "GET",
		URL:     srv.URL + "/api",
		Headers: map[string]string{"Authorization": "Bearer llm-attempt"},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAuth != "Bearer jit-wins" {
		t.Errorf("JIT header must override LLM header: got %q", gotAuth)
	}
}

// TestHTTPRequest_NoInjectorNoop guards backward compatibility.
func TestHTTPRequest_NoInjectorNoop(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tool := &httpRequestTool{} // no injector
	args, _ := json.Marshal(httpRequestInput{
		Method:  "GET",
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer llm-supplied"},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Without an injector, the LLM-supplied header stays.
	if gotAuth != "Bearer llm-supplied" {
		t.Errorf("no-injector path corrupted headers: got %q", gotAuth)
	}
}

// TestHTTPRequest_InjectorSkipsNonMatchingTool verifies tool scoping.
// A spec targeting cli_execute must not fire for http_request.
func TestHTTPRequest_InjectorSkipsNonMatchingTool(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	sink := &captureSink{}
	inj, _ := credentials.NewInjector(
		context.Background(),
		credentials.DefaultRegistry,
		[]credentials.CredentialSpec{{
			Tool:     "cli_execute", // NOT http_request
			Provider: "static",
			Spec:     json.RawMessage(`{"headers": {"Authorization": "must-not-appear"}}`),
		}},
		sink,
	)

	tool := (&httpRequestTool{}).WithCredentialInjector(inj)
	args, _ := json.Marshal(httpRequestInput{Method: "GET", URL: srv.URL})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(gotAuth, "must-not-appear") {
		t.Errorf("cli_execute-scoped spec leaked to http_request: %q", gotAuth)
	}
	// No credential_issued should have fired since no spec matched.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, e := range sink.events {
		if e.name == "credential_issued" {
			t.Errorf("unexpected credential_issued for non-matching tool")
		}
	}
}
