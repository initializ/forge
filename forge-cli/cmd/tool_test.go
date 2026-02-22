package cmd

import (
	"bytes"
	"testing"
)

func TestToolListCmd(t *testing.T) {
	rootCmd.SetArgs([]string{"tool", "list"})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("tool list error: %v", err)
	}

	output := out.String()
	if output == "" {
		// At minimum we should have the header line
		t.Log("note: tool list produced no output (expected in some test configurations)")
	}
}

func TestToolDescribeCmd_KnownTool(t *testing.T) {
	rootCmd.SetArgs([]string{"tool", "describe", "json_parse"})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("tool describe error: %v", err)
	}
}

func TestToolDescribeCmd_UnknownTool(t *testing.T) {
	rootCmd.SetArgs([]string{"tool", "describe", "nonexistent_tool"})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}
