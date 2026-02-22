// Package crewai provides a framework plugin for CrewAI agent projects.
package crewai

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

// Plugin is the CrewAI framework plugin.
type Plugin struct{}

func (p *Plugin) Name() string { return "crewai" }

// DetectProject checks for CrewAI markers in the project directory.
func (p *Plugin) DetectProject(dir string) (bool, error) {
	// Check requirements.txt
	if found, err := fileContains(filepath.Join(dir, "requirements.txt"), "crewai"); err == nil && found {
		return true, nil
	}

	// Check pyproject.toml
	if found, err := fileContains(filepath.Join(dir, "pyproject.toml"), "crewai"); err == nil && found {
		return true, nil
	}

	// Scan top-level .py files for crewai imports
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
		if strings.Contains(content, "from crewai import") || strings.Contains(content, "import crewai") {
			return true, nil
		}
	}

	return false, nil
}

var (
	reAgentRole      = regexp.MustCompile(`Agent\(\s*role\s*=\s*"([^"]+)"`)
	reAgentGoal      = regexp.MustCompile(`goal\s*=\s*"([^"]+)"`)
	reToolClass      = regexp.MustCompile(`class\s+(\w+)\(BaseTool\)`)
	reToolName       = regexp.MustCompile(`name:\s*str\s*=\s*"([^"]+)"`)
	reToolDesc       = regexp.MustCompile(`description:\s*str\s*=\s*"([^"]+)"`)
	reAgentBackstory = regexp.MustCompile(`backstory\s*=\s*"([^"]+)"`)
)

// ExtractAgentConfig scans Python files for CrewAI patterns.
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

		// Extract agent identity
		if m := reAgentRole.FindStringSubmatch(content); m != nil {
			if cfg.Identity == nil {
				cfg.Identity = &plugins.IdentityConfig{}
			}
			cfg.Identity.Role = m[1]
		}
		if m := reAgentGoal.FindStringSubmatch(content); m != nil {
			if cfg.Identity == nil {
				cfg.Identity = &plugins.IdentityConfig{}
			}
			cfg.Identity.Goal = m[1]
		}
		if m := reAgentBackstory.FindStringSubmatch(content); m != nil {
			if cfg.Identity == nil {
				cfg.Identity = &plugins.IdentityConfig{}
			}
			cfg.Identity.Backstory = m[1]
		}

		// Extract tool classes
		classMatches := reToolClass.FindAllStringSubmatch(content, -1)
		for _, cm := range classMatches {
			tool := plugins.ToolDefinition{Name: cm[1]}

			// Try to find name and description fields near the class
			if m := reToolName.FindStringSubmatch(content); m != nil {
				tool.Name = m[1]
			}
			if m := reToolDesc.FindStringSubmatch(content); m != nil {
				tool.Description = m[1]
			}

			cfg.Tools = append(cfg.Tools, tool)
		}
	}

	// Set description from identity if available
	if cfg.Identity != nil && cfg.Identity.Goal != "" {
		cfg.Description = cfg.Identity.Goal
	}

	return cfg, nil
}

// GenerateWrapper renders the CrewAI A2A wrapper template.
func (p *Plugin) GenerateWrapper(config *plugins.AgentConfig) ([]byte, error) {
	tmplStr, err := templates.GetWrapperTemplate("crewai_wrapper.py.tmpl")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("crewai_wrapper").Parse(tmplStr)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// RuntimeDependencies returns CrewAI pip packages.
func (p *Plugin) RuntimeDependencies() []string {
	return []string{"crewai", "crewai-tools"}
}

// fileContains checks if a file exists and contains the given substring.
func fileContains(path, substr string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(data), substr), nil
}
