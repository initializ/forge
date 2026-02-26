package memory

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// Default chunking parameters (~400 tokens at ~4 chars/token).
const (
	DefaultChunkChars   = 1600
	DefaultOverlapChars = 320
)

// Chunk is a segment of a memory file for indexing and search.
type Chunk struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"` // relative file path
	Content   string    `json:"content"`
	LineStart int       `json:"line_start"`
	LineEnd   int       `json:"line_end"`
	CreatedAt time.Time `json:"created_at"`
}

// ChunkText splits text from a source file into overlapping chunks.
// chunkSize and overlap are in characters. Use 0 for defaults.
func ChunkText(text, source string, chunkSize, overlap int) []Chunk {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkChars
	}
	if overlap <= 0 {
		overlap = DefaultOverlapChars
	}
	if overlap >= chunkSize {
		overlap = chunkSize / 5
	}

	lines := strings.Split(text, "\n")
	paragraphs := splitParagraphs(lines)

	var chunks []Chunk
	var buf strings.Builder
	bufStartLine := 0
	bufEndLine := 0

	for _, p := range paragraphs {
		// If adding this paragraph exceeds chunk size, emit current buffer.
		if buf.Len() > 0 && buf.Len()+len(p.text) > chunkSize {
			chunks = append(chunks, makeChunk(buf.String(), source, bufStartLine, bufEndLine))

			// Start next chunk with overlap from the end of current buffer.
			overlapText := overlapSuffix(buf.String(), overlap)
			buf.Reset()
			buf.WriteString(overlapText)
			bufStartLine = p.lineStart
		}

		if buf.Len() == 0 {
			bufStartLine = p.lineStart
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(p.text)
		bufEndLine = p.lineEnd

		// Handle oversized paragraphs: split on sentence boundaries.
		if buf.Len() > chunkSize {
			sentences := splitSentences(buf.String())
			buf.Reset()
			for _, sent := range sentences {
				if buf.Len() > 0 && buf.Len()+len(sent) > chunkSize {
					chunks = append(chunks, makeChunk(buf.String(), source, bufStartLine, bufEndLine))
					overlapText := overlapSuffix(buf.String(), overlap)
					buf.Reset()
					buf.WriteString(overlapText)
				}
				if buf.Len() > 0 {
					buf.WriteString(" ")
				}
				buf.WriteString(sent)
			}
		}
	}

	// Emit remaining buffer.
	if buf.Len() > 0 {
		chunks = append(chunks, makeChunk(buf.String(), source, bufStartLine, bufEndLine))
	}

	return chunks
}

type paragraph struct {
	text      string
	lineStart int
	lineEnd   int
}

// splitParagraphs groups lines separated by blank lines into paragraphs.
func splitParagraphs(lines []string) []paragraph {
	var paragraphs []paragraph
	var current strings.Builder
	startLine := 0
	inParagraph := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if inParagraph {
				paragraphs = append(paragraphs, paragraph{
					text:      current.String(),
					lineStart: startLine,
					lineEnd:   i - 1,
				})
				current.Reset()
				inParagraph = false
			}
			continue
		}

		if !inParagraph {
			startLine = i
			inParagraph = true
		} else {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}

	if inParagraph {
		paragraphs = append(paragraphs, paragraph{
			text:      current.String(),
			lineStart: startLine,
			lineEnd:   len(lines) - 1,
		})
	}

	return paragraphs
}

// splitSentences splits text on sentence-ending punctuation.
func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	for i, r := range text {
		current.WriteRune(r)
		if (r == '.' || r == '!' || r == '?') && i+1 < len(text) && text[i+1] == ' ' {
			sentences = append(sentences, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, strings.TrimSpace(current.String()))
	}
	return sentences
}

// overlapSuffix returns the last `n` characters of s.
func overlapSuffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// makeChunk creates a Chunk with a deterministic ID based on source, position, and content.
func makeChunk(content, source string, lineStart, lineEnd int) Chunk {
	h := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%d:%s", source, lineStart, lineEnd, content))
	return Chunk{
		ID:        fmt.Sprintf("%x", h[:8]),
		Source:    source,
		Content:   content,
		LineStart: lineStart,
		LineEnd:   lineEnd,
		CreatedAt: time.Now().UTC(),
	}
}
