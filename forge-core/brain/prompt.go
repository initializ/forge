package brain

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/llm"
)

// FormatToolPrompt generates a system prompt section describing available tools.
// The model is instructed to output tool calls as JSON only when needed.
func FormatToolPrompt(tools []llm.ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder

	// Collect tool names for the constraint list
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Function.Name)
	}

	sb.WriteString("# Tool Use Instructions\n\n")
	sb.WriteString("You have tools available. Decide whether to use one based on these rules:\n\n")
	sb.WriteString("RESPOND WITH PLAIN TEXT (no tool) for: greetings, opinions, math, definitions, general knowledge that does not change over time.\n\n")
	sb.WriteString("USE A TOOL for: current news, weather, live data, real-time information, searching, file operations, code execution, or any request that needs up-to-date or external data.\n\n")
	sb.WriteString("You may ONLY call tools from this exact list: ")
	sb.WriteString(strings.Join(toolNames, ", "))
	sb.WriteString(". Never invent or guess tool names.\n\n")
	sb.WriteString("When you need a tool, respond with ONLY this JSON (no other text):\n")
	sb.WriteString("{\"name\": \"tool_name\", \"arguments\": {\"param1\": \"value1\"}}\n\n")
	sb.WriteString("Available tools:\n\n")

	for _, tool := range tools {
		fmt.Fprintf(&sb, "- %s: %s\n", tool.Function.Name, tool.Function.Description)
		if len(tool.Function.Parameters) > 0 && string(tool.Function.Parameters) != "null" {
			fmt.Fprintf(&sb, "  Parameters: %s\n", string(tool.Function.Parameters))
		}
	}

	return sb.String()
}

// toolCallJSON is the expected JSON structure for a tool call.
type toolCallJSON struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// parseToolCalls extracts tool calls from response text.
// It looks for JSON objects with "name" and "arguments" fields.
func parseToolCalls(text string) ([]llm.ToolCall, error) {
	var calls []llm.ToolCall

	// Try to find JSON objects in the text
	candidates := extractJSONObjects(text)

	for i, candidate := range candidates {
		var tc toolCallJSON
		if err := json.Unmarshal([]byte(candidate), &tc); err != nil {
			continue
		}

		if tc.Name == "" {
			continue
		}

		// Ensure arguments is valid JSON
		args := string(tc.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}

		calls = append(calls, llm.ToolCall{
			ID:   fmt.Sprintf("brain-tc-%d", i),
			Type: "function",
			Function: llm.FunctionCall{
				Name:      tc.Name,
				Arguments: args,
			},
		})
	}

	if len(calls) == 0 {
		return nil, fmt.Errorf("no tool calls found in response")
	}

	return calls, nil
}

// extractJSONObjects finds JSON object substrings in text.
func extractJSONObjects(text string) []string {
	var results []string

	// Strip markdown code blocks if present
	text = stripCodeBlocks(text)

	i := 0
	for i < len(text) {
		// Find the start of a JSON object
		start := strings.Index(text[i:], "{")
		if start == -1 {
			break
		}
		start += i

		// Find the matching closing brace
		end := findMatchingBrace(text, start)
		if end == -1 {
			i = start + 1
			continue
		}

		candidate := text[start : end+1]

		// Quick validation: must contain "name"
		if strings.Contains(candidate, `"name"`) {
			results = append(results, candidate)
		}

		i = end + 1
	}

	return results
}

// stripCodeBlocks removes markdown code fences from text.
func stripCodeBlocks(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inBlock = !inBlock
			continue
		}
		if inBlock || !strings.HasPrefix(trimmed, "```") {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// findMatchingBrace finds the index of the closing brace matching the opening brace at pos.
func findMatchingBrace(text string, pos int) int {
	if pos >= len(text) || text[pos] != '{' {
		return -1
	}

	depth := 0
	inString := false
	escaped := false

	for i := pos; i < len(text); i++ {
		ch := text[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && inString {
			escaped = true
			continue
		}

		if ch == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}
