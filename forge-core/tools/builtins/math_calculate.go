package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/initializ/forge/forge-core/tools"
)

type mathCalculateTool struct{}

type mathCalculateInput struct {
	Expression string `json:"expression"`
}

func (t *mathCalculateTool) Name() string             { return "math_calculate" }
func (t *mathCalculateTool) Description() string      { return "Evaluate arithmetic expressions safely" }
func (t *mathCalculateTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *mathCalculateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"expression": {"type": "string", "description": "Mathematical expression (e.g. '2 + 3 * 4', 'sqrt(16)', 'pow(2,10)')"}
		},
		"required": ["expression"]
	}`)
}

func (t *mathCalculateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input mathCalculateInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	result, err := evalExpr(input.Expression)
	if err != nil {
		return "", err
	}

	// Format nicely: if it's a whole number, show without decimal
	if result == math.Trunc(result) && !math.IsInf(result, 0) {
		return strconv.FormatInt(int64(result), 10), nil
	}
	return strconv.FormatFloat(result, 'g', -1, 64), nil
}

// Simple recursive descent parser for arithmetic expressions.
// Supports: +, -, *, /, parentheses, sqrt(), pow(), abs(), unary minus
type parser struct {
	input string
	pos   int
}

func evalExpr(expr string) (float64, error) {
	p := &parser{input: strings.TrimSpace(expr)}
	result, err := p.parseExpression()
	if err != nil {
		return 0, err
	}
	p.skipSpaces()
	if p.pos < len(p.input) {
		return 0, fmt.Errorf("unexpected character at position %d: %q", p.pos, string(p.input[p.pos]))
	}
	return result, nil
}

func (p *parser) parseExpression() (float64, error) {
	return p.parseAddSub()
}

func (p *parser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}

	for {
		p.skipSpaces()
		if p.pos >= len(p.input) {
			return left, nil
		}
		op := p.input[p.pos]
		if op != '+' && op != '-' {
			return left, nil
		}
		p.pos++
		right, err := p.parseMulDiv()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *parser) parseMulDiv() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}

	for {
		p.skipSpaces()
		if p.pos >= len(p.input) {
			return left, nil
		}
		op := p.input[p.pos]
		if op != '*' && op != '/' {
			return left, nil
		}
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		}
	}
}

func (p *parser) parseUnary() (float64, error) {
	p.skipSpaces()
	if p.pos < len(p.input) && p.input[p.pos] == '-' {
		p.pos++
		val, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		return -val, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (float64, error) {
	p.skipSpaces()
	if p.pos >= len(p.input) {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	// Parenthesized expression
	if p.input[p.pos] == '(' {
		p.pos++
		val, err := p.parseExpression()
		if err != nil {
			return 0, err
		}
		p.skipSpaces()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return val, nil
	}

	// Function call
	if unicode.IsLetter(rune(p.input[p.pos])) {
		return p.parseFunction()
	}

	// Number
	return p.parseNumber()
}

func (p *parser) parseFunction() (float64, error) {
	start := p.pos
	for p.pos < len(p.input) && (unicode.IsLetter(rune(p.input[p.pos])) || unicode.IsDigit(rune(p.input[p.pos]))) {
		p.pos++
	}
	name := strings.ToLower(p.input[start:p.pos])
	p.skipSpaces()

	if p.pos >= len(p.input) || p.input[p.pos] != '(' {
		return 0, fmt.Errorf("expected '(' after function %q", name)
	}
	p.pos++ // skip '('

	args, err := p.parseFuncArgs()
	if err != nil {
		return 0, err
	}

	switch name {
	case "sqrt":
		if len(args) != 1 {
			return 0, fmt.Errorf("sqrt requires 1 argument")
		}
		return math.Sqrt(args[0]), nil
	case "pow":
		if len(args) != 2 {
			return 0, fmt.Errorf("pow requires 2 arguments")
		}
		return math.Pow(args[0], args[1]), nil
	case "abs":
		if len(args) != 1 {
			return 0, fmt.Errorf("abs requires 1 argument")
		}
		return math.Abs(args[0]), nil
	case "min":
		if len(args) != 2 {
			return 0, fmt.Errorf("min requires 2 arguments")
		}
		return math.Min(args[0], args[1]), nil
	case "max":
		if len(args) != 2 {
			return 0, fmt.Errorf("max requires 2 arguments")
		}
		return math.Max(args[0], args[1]), nil
	default:
		return 0, fmt.Errorf("unknown function: %q", name)
	}
}

func (p *parser) parseFuncArgs() ([]float64, error) {
	var args []float64
	p.skipSpaces()
	if p.pos < len(p.input) && p.input[p.pos] == ')' {
		p.pos++
		return args, nil
	}

	for {
		val, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, val)
		p.skipSpaces()
		if p.pos >= len(p.input) {
			return nil, fmt.Errorf("missing closing parenthesis in function call")
		}
		if p.input[p.pos] == ')' {
			p.pos++
			return args, nil
		}
		if p.input[p.pos] == ',' {
			p.pos++
			continue
		}
		return nil, fmt.Errorf("unexpected character in function args: %q", string(p.input[p.pos]))
	}
}

func (p *parser) parseNumber() (float64, error) {
	p.skipSpaces()
	start := p.pos
	for p.pos < len(p.input) && (unicode.IsDigit(rune(p.input[p.pos])) || p.input[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("expected number at position %d", p.pos)
	}
	return strconv.ParseFloat(p.input[start:p.pos], 64)
}

func (p *parser) skipSpaces() {
	for p.pos < len(p.input) && p.input[p.pos] == ' ' {
		p.pos++
	}
}
