package builtins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

func TestRegisterAll(t *testing.T) {
	reg := tools.NewRegistry()
	if err := RegisterAll(reg); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	expected := []string{
		"http_request", "json_parse", "csv_parse",
		"datetime_now", "uuid_generate", "math_calculate", "web_search",
	}
	for _, name := range expected {
		if reg.Get(name) == nil {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestGetByName(t *testing.T) {
	tool := GetByName("json_parse")
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name() != "json_parse" {
		t.Errorf("name: got %q", tool.Name())
	}

	if GetByName("nonexistent") != nil {
		t.Error("expected nil for nonexistent tool")
	}
}

func TestHTTPRequestTool(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"hello"}`)) //nolint:errcheck
	}))
	defer ts.Close()

	tool := GetByName("http_request")
	args, _ := json.Marshal(map[string]any{
		"method": "GET",
		"url":    ts.URL,
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result should contain response: %q", result)
	}
}

func TestJSONParseTool(t *testing.T) {
	tool := GetByName("json_parse")

	t.Run("parse", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{
			"data": `{"name":"John","age":"30"}`,
		})
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !strings.Contains(result, "John") {
			t.Errorf("result should contain name: %q", result)
		}
	})

	t.Run("query", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{
			"data":  `{"user":{"name":"Jane"}}`,
			"query": "user.name",
		})
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !strings.Contains(result, "Jane") {
			t.Errorf("result should contain queried value: %q", result)
		}
	})
}

func TestCSVParseTool(t *testing.T) {
	tool := GetByName("csv_parse")
	args, _ := json.Marshal(map[string]any{
		"data": "name,age\nAlice,30\nBob,25",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("result should contain Alice: %q", result)
	}
}

func TestDatetimeNowTool(t *testing.T) {
	tool := GetByName("datetime_now")
	args, _ := json.Marshal(map[string]string{
		"format":   "date",
		"timezone": "UTC",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// Should be in YYYY-MM-DD format
	if len(result) != 10 || result[4] != '-' {
		t.Errorf("unexpected date format: %q", result)
	}
}

func TestUUIDGenerateTool(t *testing.T) {
	tool := GetByName("uuid_generate")
	result, err := tool.Execute(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// UUID v4 format: 8-4-4-4-12
	if len(result) != 36 || result[8] != '-' || result[13] != '-' || result[18] != '-' || result[23] != '-' {
		t.Errorf("invalid UUID format: %q", result)
	}
}

func TestMathCalculateTool(t *testing.T) {
	tool := GetByName("math_calculate")

	tests := []struct {
		expr string
		want string
	}{
		{"2 + 3", "5"},
		{"10 - 4", "6"},
		{"3 * 4", "12"},
		{"15 / 3", "5"},
		{"(2 + 3) * 4", "20"},
		{"sqrt(16)", "4"},
		{"pow(2, 10)", "1024"},
		{"abs(-5)", "5"},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"expression": tt.expr})
			result, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("Execute error for %q: %v", tt.expr, err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestMathCalculateTool_DivisionByZero(t *testing.T) {
	tool := GetByName("math_calculate")
	args, _ := json.Marshal(map[string]string{"expression": "1 / 0"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Error("expected error for division by zero")
	}
}

func TestWebSearchTool_NoKey(t *testing.T) {
	orig := os.Getenv("PERPLEXITY_API_KEY")
	_ = os.Unsetenv("PERPLEXITY_API_KEY")
	defer func() {
		if orig != "" {
			_ = os.Setenv("PERPLEXITY_API_KEY", orig)
		}
	}()

	tool := GetByName("web_search")
	args, _ := json.Marshal(map[string]string{"query": "test"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "PERPLEXITY_API_KEY") {
		t.Errorf("expected missing key message, got: %q", result)
	}
}

func TestAllToolsHaveCategory(t *testing.T) {
	for _, tool := range All() {
		if tool.Category() != tools.CategoryBuiltin {
			t.Errorf("tool %q category: got %q, want %q", tool.Name(), tool.Category(), tools.CategoryBuiltin)
		}
	}
}
