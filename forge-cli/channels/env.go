package channels

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnvVarsFromConfig returns the sorted, deduped union of env-var names that
// the configured channel adapters require. The canonical source is the
// project's per-channel YAML — every setting key ending in "_env" declares
// an env var name (e.g. "bot_token_env: SLACK_BOT_TOKEN"), matching the
// runtime contract used by channels.ResolveEnvVars.
//
// channelNames are the values from forge.yaml's `channels:` list. For each
// name, the file workDir/<name>-config.yaml is consulted. A missing file is
// reported via missing[] and produces no env vars; parse errors are returned.
//
// This is the single canonical source — build stages, container packaging,
// and any other tooling that needs to know "which env vars do my channels
// require" should call this. Adding a new channel adapter requires no edits
// here: it ships its own *-config.yaml template and the helper picks it up.
func EnvVarsFromConfig(workDir string, channelNames []string) (envVars []string, missing []string, err error) {
	seen := make(map[string]bool)
	for _, name := range channelNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		path := filepath.Join(workDir, name+"-config.yaml")
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			missing = append(missing, name)
			continue
		}
		cfg, loadErr := LoadChannelConfig(path)
		if loadErr != nil {
			return nil, missing, fmt.Errorf("channel %q: %w", name, loadErr)
		}
		for k, v := range cfg.Settings {
			base, ok := strings.CutSuffix(k, "_env")
			if !ok || base == "" {
				continue
			}
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			envVars = append(envVars, v)
		}
	}
	sort.Strings(envVars)
	return envVars, missing, nil
}
