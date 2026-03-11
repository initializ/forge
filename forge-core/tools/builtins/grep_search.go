package builtins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

// Directories to skip during search.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
	"dist":         true,
	"build":        true,
}

type grepSearchTool struct {
	pathValidator *PathValidator
}

func (t *grepSearchTool) Name() string { return "grep_search" }
func (t *grepSearchTool) Description() string {
	return "Search file contents using a regex pattern. Uses ripgrep (rg) if available, otherwise falls back to a Go-based search. Returns matches in file:line:content format."
}
func (t *grepSearchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *grepSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Regex pattern to search for"
			},
			"path": {
				"type": "string",
				"description": "Directory or file to search in (relative to project root). Default: project root"
			},
			"include": {
				"type": "string",
				"description": "Glob pattern to filter files (e.g. '*.go', '*.ts')"
			},
			"exclude": {
				"type": "string",
				"description": "Glob pattern to exclude files (e.g. '*.test.ts', 'test/**')"
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of output lines to return. Default: 50"
			},
			"context": {
				"type": "integer",
				"description": "Number of context lines to show before and after each match. Default: 0"
			}
		},
		"required": ["pattern"]
	}`)
}

func (t *grepSearchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Include    string `json:"include"`
		Exclude    string `json:"exclude"`
		MaxResults int    `json:"max_results"`
		Context    int    `json:"context"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if strings.TrimSpace(input.Pattern) == "" {
		return "", fmt.Errorf("pattern is required")
	}

	searchPath, err := t.pathValidator.Resolve(input.Path)
	if err != nil {
		return "", err
	}

	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}

	// Try ripgrep first.
	if rgPath, lookErr := exec.LookPath("rg"); lookErr == nil {
		result, rgErr := t.searchWithRipgrep(rgPath, searchPath, input.Pattern, input.Include, input.Exclude, input.Context, maxResults)
		if rgErr == nil {
			return result, nil
		}
		// Fall through to Go-based search on ripgrep error.
	}

	return t.searchWithGo(searchPath, input.Pattern, input.Include, input.Exclude, input.Context, maxResults)
}

func (t *grepSearchTool) searchWithRipgrep(rgPath, searchPath, pattern, include, exclude string, contextLines, maxResults int) (string, error) {
	args := []string{
		"--no-heading",
		"--line-number",
		"--color", "never",
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	if exclude != "" {
		args = append(args, "--glob", "!"+exclude)
	}
	if contextLines > 0 {
		args = append(args, "-C", fmt.Sprintf("%d", contextLines))
	}
	args = append(args, pattern, searchPath)

	cmd := exec.Command(rgPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Exit code 1 means no matches — not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "(no matches found)", nil
		}
		return "", fmt.Errorf("ripgrep error: %s", stderr.String())
	}

	result := stdout.String()
	if result == "" {
		return "(no matches found)", nil
	}

	// Make paths relative to workDir.
	result = t.relativizePaths(result)

	// Enforce total output line limit.
	lines := strings.Split(result, "\n")
	if len(lines) > maxResults {
		result = strings.Join(lines[:maxResults], "\n") + "\n... (more results not shown)"
	}

	return TruncateOutput(result), nil
}

func (t *grepSearchTool) searchWithGo(searchPath, pattern, include, exclude string, contextLines, maxResults int) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}

	var sb strings.Builder
	totalLines := 0

	walkErr := filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if totalLines >= maxResults {
			return filepath.SkipAll
		}

		// Apply include filter.
		if include != "" {
			matched, matchErr := filepath.Match(include, d.Name())
			if matchErr != nil || !matched {
				return nil
			}
		}

		// Apply exclude filter.
		if exclude != "" {
			matched, _ := filepath.Match(exclude, d.Name())
			if matched {
				return nil
			}
		}

		// Skip binary files (check first 512 bytes).
		if isBinaryFile(path) {
			return nil
		}

		relPath, _ := filepath.Rel(t.pathValidator.WorkDir(), path)
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()

		// Read all lines for context support.
		var allLines []string
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			allLines = append(allLines, scanner.Text())
		}

		// Find matching lines and emit with context.
		lastPrinted := -1
		for lineIdx, line := range allLines {
			if !re.MatchString(line) {
				continue
			}
			if totalLines >= maxResults {
				break
			}

			startCtx := lineIdx - contextLines
			if startCtx < 0 {
				startCtx = 0
			}
			endCtx := lineIdx + contextLines
			if endCtx >= len(allLines) {
				endCtx = len(allLines) - 1
			}

			// Add group separator if there's a gap from last printed block.
			if contextLines > 0 && lastPrinted >= 0 && startCtx > lastPrinted+1 {
				sb.WriteString("--\n")
				totalLines++
				if totalLines >= maxResults {
					break
				}
			}

			for i := startCtx; i <= endCtx; i++ {
				if i <= lastPrinted {
					continue
				}
				if totalLines >= maxResults {
					break
				}
				sep := "-"
				if i == lineIdx {
					sep = ":"
				}
				fmt.Fprintf(&sb, "%s:%d%s%s\n", relPath, i+1, sep, allLines[i])
				totalLines++
				lastPrinted = i
			}
		}
		return nil
	})

	if walkErr != nil {
		return "", fmt.Errorf("search error: %w", walkErr)
	}

	if sb.Len() == 0 {
		return "(no matches found)", nil
	}

	return TruncateOutput(sb.String()), nil
}

func (t *grepSearchTool) relativizePaths(output string) string {
	prefix := t.pathValidator.WorkDir() + string(filepath.Separator)
	return strings.ReplaceAll(output, prefix, "")
}

// isBinaryFile checks if a file appears to be binary by reading the first 512 bytes.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}

	return bytes.ContainsRune(buf[:n], 0)
}
