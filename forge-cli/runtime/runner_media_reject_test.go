package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

// TestRunner_RejectsInboundFileParts is the #255 footgun regression: a client
// attaching a file (image/document) part used to get a 200 and a plausible
// answer that silently ignored the attachment, because the runtime projects
// only text/data parts into the prompt. Now every send path rejects a file
// part loudly (JSON-RPC error / HTTP 400) naming the mime type, while a
// text-only message still succeeds.
func TestRunner_RejectsInboundFileParts(t *testing.T) {
	dir := t.TempDir()
	cfg := &types.ForgeConfig{
		AgentID:    "media-reject-test",
		Version:    "0.1.0",
		Framework:  "forge",
		Entrypoint: "python main.py",
		Tools:      []types.ToolRef{{Name: "search"}},
	}
	port, err := findFreePort()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Config: cfg, WorkDir: dir, Port: port, MockTools: true})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	baseURL := fmt.Sprintf("http://localhost:%d", port)
	waitForServer(t, baseURL, 5*time.Second)
	token, err := auth.LoadToken(dir)
	if err != nil {
		t.Fatalf("loading auth token: %v", err)
	}

	fileMsg := a2a.Message{
		Role: a2a.MessageRoleUser,
		Parts: []a2a.Part{
			a2a.NewTextPart("describe this"),
			a2a.NewFilePart(a2a.FileContent{Name: "photo.png", MimeType: "image/png", Bytes: []byte{0x89, 0x50, 0x4e, 0x47}}),
		},
	}

	t.Run("json-rpc tasks/send rejects file part", func(t *testing.T) {
		rpcReq := a2a.JSONRPCRequest{
			JSONRPC: "2.0", ID: "1", Method: "tasks/send",
			Params: mustMarshal(a2a.SendTaskParams{ID: "t-media-1", Message: fileMsg}),
		}
		body, _ := json.Marshal(rpcReq)
		resp, err := authPost(baseURL+"/", token, body)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var rpcResp a2a.JSONRPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if rpcResp.Error == nil {
			t.Fatalf("expected a JSON-RPC error rejecting the file part, got result: %+v", rpcResp.Result)
		}
		if !strings.Contains(rpcResp.Error.Message, "image/png") {
			t.Errorf("error message should name the rejected mime type; got %q", rpcResp.Error.Message)
		}
	})

	t.Run("REST /tasks/send rejects file part with 400", func(t *testing.T) {
		body, _ := json.Marshal(restBody("t-media-2", fileMsg))
		resp, err := authPost(baseURL+"/tasks/send", token, body)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		var errBody map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if !strings.Contains(errBody["error"], "image/png") {
			t.Errorf("400 body should name the rejected mime type; got %q", errBody["error"])
		}
	})

	t.Run("text-only message still succeeds", func(t *testing.T) {
		textMsg := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hello")}}
		body, _ := json.Marshal(restBody("t-text-ok", textMsg))
		resp, err := authPost(baseURL+"/tasks/send", token, body)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("text-only status = %d, want 200 (media gate must not affect text)", resp.StatusCode)
		}
	})
}

// restBody builds the REST {"task":{"id","message"}} envelope.
func restBody(id string, msg a2a.Message) map[string]any {
	return map[string]any{"task": map[string]any{"id": id, "message": msg}}
}
