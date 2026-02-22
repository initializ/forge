package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestCLIExecute_Name(t *testing.T) {
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"echo"},
	})
	if got := tool.Name(); got != "cli_execute" {
		t.Errorf("Name() = %q, want %q", got, "cli_execute")
	}
}

func TestCLIExecute_Category(t *testing.T) {
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"echo"},
	})
	if got := tool.Category(); got != "builtin" {
		t.Errorf("Category() = %q, want %q", got, "builtin")
	}
}

func TestCLIExecute_DynamicSchema(t *testing.T) {
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"curl", "jq"},
	})
	schema := tool.InputSchema()

	var parsed map[string]any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("InputSchema() returned invalid JSON: %v", err)
	}

	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema() missing 'properties'")
	}
	binaryProp, ok := props["binary"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema() missing 'binary' property")
	}
	enumVals, ok := binaryProp["enum"].([]any)
	if !ok {
		t.Fatal("InputSchema() missing 'enum' on binary property")
	}
	if len(enumVals) != 2 {
		t.Errorf("InputSchema() enum has %d items, want 2", len(enumVals))
	}

	found := map[string]bool{}
	for _, v := range enumVals {
		found[v.(string)] = true
	}
	if !found["curl"] || !found["jq"] {
		t.Errorf("InputSchema() enum = %v, want [curl, jq]", enumVals)
	}
}

func TestCLIExecute_Allowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo behavior differs on Windows")
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"echo"},
	})

	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "echo",
		Args:   []string{"hello"},
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var res cliExecuteResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if got := strings.TrimSpace(res.Stdout); got != "hello" {
		t.Errorf("stdout = %q, want %q", got, "hello")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", res.ExitCode)
	}
}

func TestCLIExecute_Disallowed(t *testing.T) {
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"echo"},
	})

	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "rm",
	})

	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("Execute() expected error for disallowed binary, got nil")
	}
	if !strings.Contains(err.Error(), "not in the allowed list") {
		t.Errorf("error = %q, want it to mention 'not in the allowed list'", err.Error())
	}
}

func TestCLIExecute_ShellInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("echo behavior differs on Windows")
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"echo"},
	})

	tests := []struct {
		name string
		arg  string
	}{
		{"backtick", "hello `whoami`"},
		{"command_substitution", "hello $(whoami)"},
		{"newline", "hello\nworld"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(cliExecuteArgs{
				Binary: "echo",
				Args:   []string{tt.arg},
			})

			_, err := tool.Execute(context.Background(), args)
			if err == nil {
				t.Errorf("Execute() expected error for injection arg %q, got nil", tt.arg)
			}
		})
	}
}

func TestCLIExecute_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep not available on Windows")
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"sleep"},
		TimeoutSeconds:  1,
	})

	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "sleep",
		Args:   []string{"10"},
	})

	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("Execute() expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to mention 'timed out'", err.Error())
	}
}

func TestCLIExecute_OutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dd not available on Windows")
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"dd"},
		MaxOutputBytes:  100, // very small limit
	})

	// dd will produce more than 100 bytes of output
	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "dd",
		Args:   []string{"if=/dev/zero", "bs=1024", "count=1"},
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var res cliExecuteResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if !res.Truncated {
		t.Error("expected truncated = true")
	}
}

func TestCLIExecute_MissingBinary(t *testing.T) {
	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"nonexistent_binary_xyz_12345"},
	})

	avail, missing := tool.Availability()
	if len(avail) != 0 {
		t.Errorf("Availability() available = %v, want empty", avail)
	}
	if len(missing) != 1 || missing[0] != "nonexistent_binary_xyz_12345" {
		t.Errorf("Availability() missing = %v, want [nonexistent_binary_xyz_12345]", missing)
	}

	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "nonexistent_binary_xyz_12345",
	})

	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("Execute() expected error for missing binary, got nil")
	}
	if !strings.Contains(err.Error(), "not found on this system") {
		t.Errorf("error = %q, want it to mention 'not found on this system'", err.Error())
	}
}

func TestCLIExecute_EnvIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env command differs on Windows")
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"env"},
		EnvPassthrough:  []string{"FORGE_TEST_VAR"},
	})

	// Set a test var and a var that should NOT pass through
	t.Setenv("FORGE_TEST_VAR", "test_value")
	t.Setenv("SECRET_VAR", "should_not_appear")

	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "env",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var res cliExecuteResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Check passthrough var is present
	if !strings.Contains(res.Stdout, "FORGE_TEST_VAR=test_value") {
		t.Error("expected FORGE_TEST_VAR=test_value in env output")
	}

	// Check secret var is NOT present
	if strings.Contains(res.Stdout, "SECRET_VAR") {
		t.Error("SECRET_VAR should not appear in isolated env output")
	}

	// Check only expected vars are present
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) < 2 {
			continue
		}
		key := parts[0]
		allowed := map[string]bool{
			"PATH": true, "HOME": true, "LANG": true, "FORGE_TEST_VAR": true,
		}
		if !allowed[key] {
			t.Errorf("unexpected env var in output: %s", key)
		}
	}
}

func TestCLIExecute_Stdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cat behavior differs on Windows")
	}

	tool := NewCLIExecuteTool(CLIExecuteConfig{
		AllowedBinaries: []string{"cat"},
	})

	args, _ := json.Marshal(cliExecuteArgs{
		Binary: "cat",
		Stdin:  "hello from stdin",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var res cliExecuteResult
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if res.Stdout != "hello from stdin" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "hello from stdin")
	}
}

func TestCLIExecute_ParseConfig(t *testing.T) {
	raw := map[string]any{
		"allowed_binaries": []any{"curl", "jq", "yq"},
		"env_passthrough":  []any{"GITHUB_TOKEN"},
		"timeout":          120,
		"max_output_bytes": 1048576,
	}

	cfg := ParseCLIExecuteConfig(raw)

	if len(cfg.AllowedBinaries) != 3 {
		t.Errorf("AllowedBinaries = %v, want 3 items", cfg.AllowedBinaries)
	}
	if cfg.AllowedBinaries[0] != "curl" || cfg.AllowedBinaries[1] != "jq" || cfg.AllowedBinaries[2] != "yq" {
		t.Errorf("AllowedBinaries = %v, want [curl jq yq]", cfg.AllowedBinaries)
	}

	if len(cfg.EnvPassthrough) != 1 || cfg.EnvPassthrough[0] != "GITHUB_TOKEN" {
		t.Errorf("EnvPassthrough = %v, want [GITHUB_TOKEN]", cfg.EnvPassthrough)
	}

	if cfg.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds = %d, want 120", cfg.TimeoutSeconds)
	}

	if cfg.MaxOutputBytes != 1048576 {
		t.Errorf("MaxOutputBytes = %d, want 1048576", cfg.MaxOutputBytes)
	}

	// Test with float64 (JSON round-trip)
	rawFloat := map[string]any{
		"allowed_binaries": []any{"echo"},
		"timeout":          float64(60),
		"max_output_bytes": float64(2097152),
	}

	cfgFloat := ParseCLIExecuteConfig(rawFloat)
	if cfgFloat.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds (float64) = %d, want 60", cfgFloat.TimeoutSeconds)
	}
	if cfgFloat.MaxOutputBytes != 2097152 {
		t.Errorf("MaxOutputBytes (float64) = %d, want 2097152", cfgFloat.MaxOutputBytes)
	}
}
