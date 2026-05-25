package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/xeipuuv/gojsonschema"
)

// ValidateInputSchema checks that raw is a well-formed JSON Schema
// (draft-07 by default, but we accept any draft the loader supports —
// MCP servers in the wild use a mix). A schema that fails this check
// fails the SERVER's Discovering state, never the LLM tool call —
// downstream consumers (LLM function-calling layers) trust that any
// descriptor reaching them carries a usable schema.
//
// We don't enforce a specific MCP-mandated schema shape (e.g. "must
// be an object schema"); the LLM layer can be more lenient than we
// can be at the registry boundary.
func ValidateInputSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: inputSchema is empty", ErrProtocolError)
	}
	// gojsonschema validates a schema by loading it and inspecting
	// for parse-time errors. We don't have a document to validate
	// against — we just want to know the schema is well-formed.
	loader := gojsonschema.NewBytesLoader(raw)
	if _, err := gojsonschema.NewSchema(loader); err != nil {
		return fmt.Errorf("%w: malformed JSON Schema: %v", ErrProtocolError, err)
	}
	return nil
}
