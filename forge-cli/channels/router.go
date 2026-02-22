package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

// Router forwards channel events to an A2A agent server via JSON-RPC over HTTP.
type Router struct {
	agentURL string
	client   *http.Client
}

// NewRouter creates a Router that forwards events to the A2A server at agentURL.
func NewRouter(agentURL string) *Router {
	return &Router{
		agentURL: agentURL,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Handler returns an EventHandler suitable for passing to ChannelPlugin.Start().
func (r *Router) Handler() channels.EventHandler {
	return r.forwardToA2A
}

// forwardToA2A sends a tasks/send JSON-RPC request to the A2A server and
// extracts the agent's response message from the returned task.
func (r *Router) forwardToA2A(ctx context.Context, event *channels.ChannelEvent) (*a2a.Message, error) {
	taskID := fmt.Sprintf("%s-%s-%d", event.Channel, event.WorkspaceID, time.Now().UnixMilli())

	params := a2a.SendTaskParams{
		ID: taskID,
		Message: a2a.Message{
			Role:  a2a.MessageRoleUser,
			Parts: []a2a.Part{a2a.NewTextPart(event.Message)},
		},
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshalling params: %w", err)
	}

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      taskID,
		Method:  "tasks/send",
		Params:  paramsJSON,
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.agentURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request to A2A server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var rpcResp a2a.JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parsing JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("A2A error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// The result is a Task; extract status.message.
	resultJSON, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return nil, fmt.Errorf("re-marshalling result: %w", err)
	}

	var task a2a.Task
	if err := json.Unmarshal(resultJSON, &task); err != nil {
		return nil, fmt.Errorf("parsing task from result: %w", err)
	}

	if task.Status.Message != nil {
		return task.Status.Message, nil
	}

	return &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("(no response)")},
	}, nil
}
