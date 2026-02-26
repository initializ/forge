package memory

import (
	"strings"
	"testing"
)

func TestChunkText_Basic(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	chunks := ChunkText(text, "test.md", 100, 20)

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// All chunks should have the correct source.
	for _, c := range chunks {
		if c.Source != "test.md" {
			t.Errorf("chunk source = %q, want %q", c.Source, "test.md")
		}
		if c.ID == "" {
			t.Error("chunk ID should not be empty")
		}
	}
}

func TestChunkText_SingleParagraph(t *testing.T) {
	text := "This is a single paragraph with no breaks."
	chunks := ChunkText(text, "test.md", 1000, 100)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Content != text {
		t.Errorf("chunk content = %q, want %q", chunks[0].Content, text)
	}
}

func TestChunkText_LargeText(t *testing.T) {
	// Create a large text that should be split into multiple chunks.
	var sb strings.Builder
	for i := range 100 {
		sb.WriteString("This is a test sentence that forms a paragraph. ")
		if i%5 == 4 {
			sb.WriteString("\n\n")
		}
	}

	chunks := ChunkText(sb.String(), "test.md", 200, 40)

	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks for large text, got %d", len(chunks))
	}

	// Chunks should have unique IDs.
	ids := make(map[string]bool)
	for _, c := range chunks {
		if ids[c.ID] {
			t.Errorf("duplicate chunk ID: %s", c.ID)
		}
		ids[c.ID] = true
	}
}

func TestChunkText_EmptyText(t *testing.T) {
	chunks := ChunkText("", "test.md", 0, 0)
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty text, got %d", len(chunks))
	}
}

func TestChunkText_Defaults(t *testing.T) {
	text := "Test paragraph."
	chunks := ChunkText(text, "test.md", 0, 0)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestSplitParagraphs(t *testing.T) {
	lines := strings.Split("First line\nstill first\n\nSecond paragraph\n\n\nThird", "\n")
	paras := splitParagraphs(lines)

	if len(paras) != 3 {
		t.Fatalf("expected 3 paragraphs, got %d", len(paras))
	}

	if paras[0].lineStart != 0 {
		t.Errorf("first paragraph lineStart = %d, want 0", paras[0].lineStart)
	}
	if !strings.Contains(paras[0].text, "First line") {
		t.Errorf("first paragraph should contain 'First line'")
	}
}

func TestOverlapSuffix(t *testing.T) {
	if got := overlapSuffix("hello world", 5); got != "world" {
		t.Errorf("overlapSuffix = %q, want %q", got, "world")
	}
	if got := overlapSuffix("hi", 10); got != "hi" {
		t.Errorf("overlapSuffix short = %q, want %q", got, "hi")
	}
}
