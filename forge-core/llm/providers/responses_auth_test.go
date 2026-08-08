package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// TestNewClient_OpenAIResponses is the core of #383: the "openai-responses"
// provider string is config-selectable and returns a Responses API client.
func TestNewClient_OpenAIResponses(t *testing.T) {
	c, err := NewClient(llm.ProviderOpenAIResponses, llm.ClientConfig{APIKey: "sk-test", Model: "gpt-5.4"})
	if err != nil {
		t.Fatalf("NewClient(openai-responses) error: %v", err)
	}
	if _, ok := c.(*ResponsesClient); !ok {
		t.Fatalf("NewClient(openai-responses) = %T, want *ResponsesClient", c)
	}
}

// TestResponsesClient_StoreFlag covers the #383 privacy opt-out: DisableStore
// sends store=false; the default leaves store unset (API default retention).
func TestResponsesClient_StoreFlag(t *testing.T) {
	tests := []struct {
		name         string
		disableStore bool
		wantStoreKey bool // whether the "store" key is present in the body
		wantStoreVal bool // its value when present
	}{
		{name: "default omits store", disableStore: false, wantStoreKey: false},
		{name: "disable_store sends false", disableStore: true, wantStoreKey: true, wantStoreVal: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			}))
			defer srv.Close()

			client := NewResponsesClient(llm.ClientConfig{
				APIKey:       "sk-test",
				Model:        "gpt-5.4",
				BaseURL:      srv.URL,
				DisableStore: tt.disableStore,
			})
			if _, err := client.Chat(context.Background(), &llm.ChatRequest{
				Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat error: %v", err)
			}
			v, present := body["store"]
			if present != tt.wantStoreKey {
				t.Fatalf("store present = %v, want %v (body=%v)", present, tt.wantStoreKey, body)
			}
			if tt.wantStoreKey {
				if got, _ := v.(bool); got != tt.wantStoreVal {
					t.Errorf("store = %v, want %v", got, tt.wantStoreVal)
				}
			}
		})
	}
}

// TestResponsesClient_AuthSchemes verifies the Responses client composes with
// the same auth_scheme values as the Chat Completions client (#383 item 5):
// the native Bearer, the additive gateway header, and gateway-header-only
// (native Bearer suppressed).
func TestResponsesClient_AuthSchemes(t *testing.T) {
	tests := []struct {
		name           string
		authScheme     string
		authHeaderName string
		wantAuthz      string // expected Authorization header
		wantGateway    string // expected value of the gateway header
		gatewayHeader  string // header name to read the gateway key from
	}{
		{
			name:          "default bearer",
			authScheme:    "",
			wantAuthz:     "Bearer sk-test",
			gatewayHeader: "apikey",
			wantGateway:   "",
		},
		{
			name:          "apikey_header additive",
			authScheme:    llm.AuthSchemeAPIKeyHeader,
			wantAuthz:     "Bearer sk-test",
			gatewayHeader: "apikey",
			wantGateway:   "sk-test",
		},
		{
			name:          "apikey_header_only suppresses bearer",
			authScheme:    llm.AuthSchemeAPIKeyHeaderOnly,
			wantAuthz:     "",
			gatewayHeader: "apikey",
			wantGateway:   "sk-test",
		},
		{
			name:           "apikey_header custom name",
			authScheme:     llm.AuthSchemeAPIKeyHeader,
			authHeaderName: "x-gateway-key",
			wantAuthz:      "Bearer sk-test",
			gatewayHeader:  "x-gateway-key",
			wantGateway:    "sk-test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuthz, gotGateway string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuthz = r.Header.Get("Authorization")
				gotGateway = r.Header.Get(tt.gatewayHeader)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			}))
			defer srv.Close()

			client := NewResponsesClient(llm.ClientConfig{
				APIKey:         "sk-test",
				Model:          "gpt-5.4",
				BaseURL:        srv.URL,
				AuthScheme:     tt.authScheme,
				AuthHeaderName: tt.authHeaderName,
			})
			if _, err := client.Chat(context.Background(), &llm.ChatRequest{
				Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat error: %v", err)
			}
			if gotAuthz != tt.wantAuthz {
				t.Errorf("Authorization = %q, want %q", gotAuthz, tt.wantAuthz)
			}
			if gotGateway != tt.wantGateway {
				t.Errorf("%s = %q, want %q", tt.gatewayHeader, gotGateway, tt.wantGateway)
			}
		})
	}
}
