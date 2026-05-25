package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// TestB9_Server_RejectsMalformedDescriptor — defense in depth: even
// before the adapter layer catches a bad descriptor, the Server's
// Discovering state should reject it so we get a clean audit event
// instead of silently filtering away tools the operator was
// expecting.
func TestB9_Server_RejectsMalformedDescriptor(t *testing.T) {
	cases := []struct {
		name    string
		toolN   string
		wantSub string
	}{
		{"empty name", "", "name is empty"},
		{"double underscore", "foo__bar", `name contains "__"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var msg JSONRPCMessage
				_ = json.NewDecoder(r.Body).Decode(&msg)
				w.Header().Set("Content-Type", "application/json")
				switch msg.Method {
				case MethodInitialize:
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
				case MethodInitialized:
					w.WriteHeader(http.StatusAccepted)
				case MethodToolsList:
					body := `{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"` + tc.toolN + `","inputSchema":{"type":"object"}}]}}`
					_, _ = w.Write([]byte(body))
				}
			}))
			defer srv.Close()

			var buf threadSafeAuditBuf
			audit := runtime.NewAuditLogger(&buf)
			s, err := NewServer(types.MCPServer{
				Name: "b9-srv", Transport: "http", URL: srv.URL,
				Tools: types.MCPToolFilter{Allow: []string{"x"}},
			}, ServerDeps{HTTPClient: srv.Client(), Audit: audit})
			if err != nil {
				t.Fatal(err)
			}
			s.backoff = []time.Duration{5 * time.Millisecond}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_ = s.Run(ctx)

			log := buf.String()
			if !strings.Contains(log, `"phase":"discover"`) {
				t.Errorf("expected phase=discover (malformed descriptor caught in Discovering), got:\n%s", log)
			}
		})
	}
}
