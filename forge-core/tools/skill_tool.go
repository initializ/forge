package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SkillTool wraps a parsed skill entry as a Tool.
// It delegates execution to a CommandExecutor, running the skill's script
// with JSON input as a positional argument.
type SkillTool struct {
	name        string
	description string
	schema      json.RawMessage
	scriptPath  string
	executor    CommandExecutor
}

// NewSkillTool creates a tool wrapper for a skill entry backed by a shell script.
func NewSkillTool(name, description, inputSpec, scriptPath string, executor CommandExecutor) *SkillTool {
	schema := InputSpecToSchema(inputSpec)
	return &SkillTool{
		name:        name,
		description: description,
		schema:      schema,
		scriptPath:  scriptPath,
		executor:    executor,
	}
}

func (t *SkillTool) Name() string                 { return t.name }
func (t *SkillTool) Description() string          { return t.description }
func (t *SkillTool) Category() Category           { return CategoryCustom }
func (t *SkillTool) InputSchema() json.RawMessage { return t.schema }

func (t *SkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.executor == nil {
		return "", fmt.Errorf("skill tool %q: no command executor configured", t.name)
	}
	// Pass JSON as a positional argument to the script (not stdin).
	// Skill scripts read input via $1, e.g.: INPUT="${1:-}"
	return t.executor.Run(ctx, "bash", []string{t.scriptPath, string(args)}, nil)
}

// InputSpecToSchema converts a skill InputSpec string (e.g. "input (string), model (string)")
// into a JSON Schema object. The first parameter is marked as required.
// Falls back to an open schema if parsing fails.
func InputSpecToSchema(spec string) json.RawMessage {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)
	}

	type prop struct {
		Type        string `json:"type"`
		Description string `json:"description,omitempty"`
	}

	properties := make(map[string]prop)
	var required []string

	parts := strings.Split(spec, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Parse "name (type)" or "name (type, required)"
		name, typeStr, hasParen := strings.Cut(part, "(")
		name = strings.TrimSpace(name)
		if !hasParen {
			// No type info, treat as string
			if name != "" {
				properties[name] = prop{Type: "string"}
				if i == 0 {
					required = append(required, name)
				}
			}
			continue
		}

		typeStr = strings.TrimRight(strings.TrimSpace(typeStr), ")")
		typeStr = strings.TrimSpace(typeStr)

		// Check for "required" marker
		isRequired := false
		if strings.Contains(typeStr, "required") {
			isRequired = true
			typeStr = strings.Replace(typeStr, "required", "", 1)
			typeStr = strings.TrimRight(strings.TrimSpace(typeStr), ",")
			typeStr = strings.TrimSpace(typeStr)
		}

		// Map type to JSON Schema type
		jsonType := mapToJSONType(typeStr)
		properties[name] = prop{Type: jsonType}

		// First parameter is always required; also mark explicit required
		if i == 0 || isRequired {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)
	}
	return data
}

func mapToJSONType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "int", "integer", "number":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "array", "list":
		return "array"
	case "object", "map":
		return "object"
	default:
		return "string"
	}
}
