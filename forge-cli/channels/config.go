package channels

import (
	"fmt"
	"os"

	"github.com/initializ/forge/forge-core/channels"
	"gopkg.in/yaml.v3"
)

// LoadChannelConfig reads and parses a channel adapter YAML config file.
func LoadChannelConfig(path string) (*channels.ChannelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading channel config %s: %w", path, err)
	}

	var cfg channels.ChannelConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing channel config %s: %w", path, err)
	}

	if cfg.Adapter == "" {
		return nil, fmt.Errorf("channel config %s: adapter is required", path)
	}

	return &cfg, nil
}
