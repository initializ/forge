package builtins

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/tools"
)

type uuidGenerateTool struct{}

func (t *uuidGenerateTool) Name() string             { return "uuid_generate" }
func (t *uuidGenerateTool) Description() string      { return "Generate a random UUID v4" }
func (t *uuidGenerateTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *uuidGenerateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

func (t *uuidGenerateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", fmt.Errorf("generating UUID: %w", err)
	}

	// Set version 4 bits
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Set variant bits
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}
