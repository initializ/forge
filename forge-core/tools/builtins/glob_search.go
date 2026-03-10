package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

type globSearchTool struct {
	pathValidator *PathValidator
}

func (t *globSearchTool) Name() string { return "glob_search" }
func (t *globSearchTool) Description() string {
	return "Find files by glob pattern (e.g. '**/*.go', 'src/**/*.ts'). Returns matching file paths sorted by modification time (most recent first)."
}
func (t *globSearchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *globSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Glob pattern to match files (supports ** for recursive matching)"
			},
			"path": {
				"type": "string",
				"description": "Directory to search in (relative to project root). Default: project root"
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return. Default: 100"
			}
		},
		"required": ["pattern"]
	}`)
}

func (t *globSearchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
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
		maxResults = 100
	}

	// Check if pattern uses ** for recursive matching.
	hasDoublestar := strings.Contains(input.Pattern, "**")

	// Extract the base filename pattern from the glob.
	// For "**/*.go", the file pattern is "*.go".
	filePattern := input.Pattern
	if hasDoublestar {
		parts := strings.Split(input.Pattern, "**")
		if len(parts) > 1 {
			filePattern = strings.TrimPrefix(parts[len(parts)-1], "/")
			filePattern = strings.TrimPrefix(filePattern, string(filepath.Separator))
		}
	}

	type fileEntry struct {
		path    string
		modTime int64
	}

	var matches []fileEntry

	walkErr := filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, relErr := filepath.Rel(t.pathValidator.WorkDir(), path)
		if relErr != nil {
			return nil
		}

		var matched bool
		if hasDoublestar {
			// For ** patterns, match the filename against the file portion.
			if filePattern == "" {
				matched = true
			} else {
				matched, _ = filepath.Match(filePattern, d.Name())
			}
		} else {
			// For non-** patterns, match against the relative path from the search dir.
			relFromSearch, _ := filepath.Rel(searchPath, path)
			matched, _ = filepath.Match(input.Pattern, relFromSearch)
			if !matched {
				// Also try matching just the filename.
				matched, _ = filepath.Match(input.Pattern, d.Name())
			}
		}

		if matched {
			info, infoErr := d.Info()
			if infoErr == nil {
				matches = append(matches, fileEntry{
					path:    relPath,
					modTime: info.ModTime().UnixNano(),
				})
			}
		}
		return nil
	})

	if walkErr != nil {
		return "", fmt.Errorf("search error: %w", walkErr)
	}

	if len(matches) == 0 {
		return "(no matches found)", nil
	}

	// Sort by modification time, most recent first.
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime > matches[j].modTime
	})

	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	var sb strings.Builder
	for _, m := range matches {
		sb.WriteString(m.path)
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}
