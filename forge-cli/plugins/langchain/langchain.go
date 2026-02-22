// Package langchain provides a framework plugin for LangChain agent projects.
package langchain

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/initializ/forge/forge-cli/templates"
	"github.com/initializ/forge/forge-core/plugins"
)

// Plugin is the LangChain framework plugin.
type Plugin struct{}

func (p *Plugin) Name() string { return "langchain" }

// DetectProject checks for LangChain markers in the project directory.
func (p *Plugin) DetectProject(dir string) (bool, error) {
	// Check requirements.txt
	if found, err := fileContains(filepath.Join(dir, "requirements.txt"), "langchain"); err == nil && found {
		return true, nil
	}

	// Check pyproject.toml
	if found, err := fileContains(filepath.Join(dir, "pyproject.toml"), "langchain"); err == nil && found {
		return true, nil
	}

	// Scan top-level .py files for langchain imports
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, "from langchain") || strings.Contains(content, "import langchain") {
			return true, nil
		}
	}

	return false, nil
}

var (
	reToolDef   = regexp.MustCompile(`@tool\s*\n\s*def\s+(\w+)\(`)
	reToolDoc   = regexp.MustCompile(`@tool\s*\n\s*def\s+\w+\([^)]*\)[^:]*:\s*\n\s*"""([^"]+)"""`)
	reModelName = regexp.MustCompile(`Chat(?:OpenAI|Anthropic)\(\s*model\s*=\s*"([^"]+)"`)
)

// ExtractAgentConfig scans Python files for LangChain patterns.
func (p *Plugin) ExtractAgentConfig(dir string) (*plugins.AgentConfig, error) {
	cfg := &plugins.AgentConfig{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return cfg, nil
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)

		// Extract @tool decorated functions
		toolMatches := reToolDef.FindAllStringSubmatch(content, -1)
		for _, m := range toolMatches {
			tool := plugins.ToolDefinition{Name: m[1]}
			cfg.Tools = append(cfg.Tools, tool)
		}

		// Extract tool docstrings
		docMatches := reToolDoc.FindAllStringSubmatch(content, -1)
		for i, m := range docMatches {
			if i < len(cfg.Tools) {
				cfg.Tools[i].Description = strings.TrimSpace(m[1])
			}
		}

		// Extract model name
		if m := reModelName.FindStringSubmatch(content); m != nil {
			cfg.Model = &plugins.PluginModelConfig{Name: m[1]}
		}
	}

	return cfg, nil
}

// GenerateWrapper renders the LangChain A2A wrapper template.
func (p *Plugin) GenerateWrapper(config *plugins.AgentConfig) ([]byte, error) {
	tmplStr, err := templates.GetWrapperTemplate("langchain_wrapper.py.tmpl")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("langchain_wrapper").Parse(tmplStr)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// RuntimeDependencies returns LangChain pip packages.
func (p *Plugin) RuntimeDependencies() []string {
	return []string{"langchain", "langchain-core", "langchain-openai"}
}

func fileContains(path, substr string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(data), substr), nil
}
