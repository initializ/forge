package tools

import (
	"io/fs"
	"strings"
)

// DiscoveredTool represents a tool found via filesystem discovery.
type DiscoveredTool struct {
	Name       string
	Path       string
	Language   string
	Entrypoint string
}

// DiscoverToolsFS scans the given fs.FS for tool scripts/modules.
// It looks for:
//   - tool_*.py, tool_*.ts, tool_*.js files
//   - */tool.py, */tool.ts, */tool.js subdirectories
func DiscoverToolsFS(fsys fs.FS) []DiscoveredTool {
	var discovered []DiscoveredTool

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() {
			// Look for tool.{py,ts,js} inside subdirectory
			for _, ext := range []string{".py", ".ts", ".js"} {
				toolFile := name + "/tool" + ext
				if _, err := fs.Stat(fsys, toolFile); err == nil {
					discovered = append(discovered, DiscoveredTool{
						Name:       name,
						Path:       toolFile,
						Language:   langFromExt(ext),
						Entrypoint: toolFile,
					})
					break
				}
			}
			continue
		}

		// Look for tool_*.{py,ts,js} files
		for _, ext := range []string{".py", ".ts", ".js"} {
			if strings.HasPrefix(name, "tool_") && strings.HasSuffix(name, ext) {
				toolName := strings.TrimSuffix(strings.TrimPrefix(name, "tool_"), ext)
				discovered = append(discovered, DiscoveredTool{
					Name:       toolName,
					Path:       name,
					Language:   langFromExt(ext),
					Entrypoint: name,
				})
				break
			}
		}
	}

	return discovered
}

func langFromExt(ext string) string {
	switch ext {
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".js":
		return "javascript"
	default:
		return "unknown"
	}
}
