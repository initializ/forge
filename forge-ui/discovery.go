package forgeui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/types"
)

// Scanner discovers agents in a workspace directory.
type Scanner struct {
	rootDir string
}

// NewScanner creates a Scanner for the given workspace root.
func NewScanner(rootDir string) *Scanner {
	return &Scanner{rootDir: rootDir}
}

// Scan discovers agents by looking for forge.yaml in the root directory
// and each immediate subdirectory. Returns a map keyed by agent ID.
func (s *Scanner) Scan() (map[string]*AgentInfo, error) {
	agents := make(map[string]*AgentInfo)

	// Check root directory
	if info, err := s.scanDir(s.rootDir); err == nil && info != nil {
		agents[info.ID] = info
	}

	// Check immediate subdirectories
	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		return agents, nil // return whatever we found
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden directories
		if strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(s.rootDir, name)
		info, err := s.scanDir(dir)
		if err != nil || info == nil {
			continue
		}
		agents[info.ID] = info
	}

	return agents, nil
}

// scanDir reads forge.yaml from a directory and returns AgentInfo.
func (s *Scanner) scanDir(dir string) (*AgentInfo, error) {
	configPath := filepath.Join(dir, "forge.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	cfg, err := types.ParseForgeConfig(data)
	if err != nil {
		return nil, err
	}

	toolNames := make([]string, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		toolNames = append(toolNames, t.Name)
	}

	skillCount := countSkills(dir)

	info := &AgentInfo{
		ID:        cfg.AgentID,
		Version:   cfg.Version,
		Framework: cfg.Framework,
		Model: AgentModel{
			Provider: cfg.Model.Provider,
			Name:     cfg.Model.Name,
		},
		Tools:           toolNames,
		Channels:        cfg.Channels,
		Skills:          skillCount,
		Directory:       dir,
		Status:          StateStopped,
		NeedsPassphrase: needsPassphrase(cfg, dir),
	}

	return info, nil
}

// needsPassphrase returns true if the agent uses encrypted-file secrets
// and a secrets.enc file exists.
func needsPassphrase(cfg *types.ForgeConfig, dir string) bool {
	for _, p := range cfg.Secrets.Providers {
		if p == "encrypted-file" {
			// Check if agent-local secrets file exists.
			localPath := filepath.Join(dir, ".forge", "secrets.enc")
			if _, err := os.Stat(localPath); err == nil {
				return true
			}
			// Check global secrets file.
			home, err := os.UserHomeDir()
			if err == nil {
				globalPath := filepath.Join(home, ".forge", "secrets.enc")
				if _, err := os.Stat(globalPath); err == nil {
					return true
				}
			}
		}
	}
	return false
}

// countSkills counts skill directories under dir/skills/ by looking for
// directories containing a SKILL.md file.
func countSkills(dir string) int {
	skillsDir := filepath.Join(dir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return 0
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillFile); err == nil {
			count++
		}
	}
	return count
}
