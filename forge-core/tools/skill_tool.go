package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SkillTool wraps a parsed skill entry as a Tool. Execution shape is
// pinned at construction time via NewSkillTool (bash + script path) or
// NewBinarySkillTool (external executable). Both pass the JSON input
// as a single positional argument so script-side and binary-side share
// the same `argv[1] == JSON` contract.
type SkillTool struct {
	name        string
	description string
	schema      json.RawMessage
	// command is the executable handed to CommandExecutor.Run — "bash"
	// for script-backed skills, the resolved binary path for binary
	// skills.
	command string
	// argsPrefix is everything before the JSON-args positional. For
	// scripts: [scriptPath] (so the final argv is [bash, scriptPath, json]).
	// For binaries: nil (so the final argv is [binary, json]).
	argsPrefix []string
	executor   CommandExecutor
}

// NewSkillTool creates a tool wrapper for a skill backed by a shell
// script. The compiled `command` is `bash`; argv is
// `[bash <scriptPath> <jsonArgs>]`.
func NewSkillTool(name, description, inputSpec, scriptPath string, executor CommandExecutor) *SkillTool {
	return &SkillTool{
		name:        name,
		description: description,
		schema:      InputSpecToSchema(inputSpec),
		command:     "bash",
		argsPrefix:  []string{scriptPath},
		executor:    executor,
	}
}

// NewBinarySkillTool creates a tool wrapper for a skill backed by an
// external binary. The compiled `command` is the binary path (typically
// resolved via `exec.LookPath` by the caller); argv is
// `[<binary> <jsonArgs>]`. The CommandExecutor's trace-context env
// injection (issue #182) lets the binary's own spans nest under the
// parent agent's `tool.<name>` span via TRACEPARENT.
//
// Use this when the skill IS the binary — infil, an LLM CLI, a Python
// or Go executable. Use NewSkillTool when the skill body is bash and
// gets materialized into a script file at agent startup.
func NewBinarySkillTool(name, description, inputSpec, binaryPath string, executor CommandExecutor) *SkillTool {
	return &SkillTool{
		name:        name,
		description: description,
		schema:      InputSpecToSchema(inputSpec),
		command:     binaryPath,
		argsPrefix:  nil,
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
	// argv = command + argsPrefix + [JSON]. The JSON is always the last
	// positional so skill scripts read it via $1 (after the script path
	// position) and binaries read it via $1 (no path prefix). Identical
	// stdin/stdout contract either way.
	finalArgs := make([]string, 0, len(t.argsPrefix)+1)
	finalArgs = append(finalArgs, t.argsPrefix...)
	finalArgs = append(finalArgs, string(args))
	return t.executor.Run(ctx, t.command, finalArgs, nil)
}

// splitTopLevel splits on commas that are not inside parentheses, so a
// "(type, required)" annotation stays attached to its parameter.
func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// schemaPropertyKeyRe is the LLM providers' tool input_schema property-key
// constraint (Anthropic: ^[a-zA-Z0-9_.-]{1,64}$ — the strictest in use). A
// single violating key makes the provider reject the ENTIRE messages request,
// bricking every call the agent makes — not just the one tool (field-hit
// 2026-07-22: a skill param named "pod name" → 400 on every task).
var schemaPropertyKeyRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// InvalidSchemaPropertyKeys returns the top-level property keys of a tool
// input schema that violate the provider constraint, sorted for deterministic
// messages. Nil/unparseable schemas and schemas without properties return nil
// (nothing to validate — the provider accepts an empty object schema).
func InvalidSchemaPropertyKeys(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil
	}
	var bad []string
	for k := range doc.Properties {
		if !schemaPropertyKeyRe.MatchString(k) {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	return bad
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

	// Split on top-level commas only: the platform materializer (and common
	// hand-authoring) writes "`pod_name` (string, required), `ns` (string)" —
	// a naive split breaks inside the parens and manufactures a bogus
	// "required)" property, and the backticks ride into the schema key. Both
	// violate the LLM provider's property-key pattern and brick every call
	// (field-hit 2026-07-22).
	parts := splitTopLevel(spec)
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Parse "name (type)" or "name (type, required)"
		name, typeStr, hasParen := strings.Cut(part, "(")
		name = strings.Trim(strings.TrimSpace(name), "`\"'")
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
