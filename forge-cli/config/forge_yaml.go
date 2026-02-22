package config

import (
	"fmt"
	"os"

	"github.com/initializ/forge/forge-core/types"
)

// LoadForgeConfig reads and parses a forge.yaml file from the given path.
func LoadForgeConfig(path string) (*types.ForgeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading forge config %s: %w", path, err)
	}
	return types.ParseForgeConfig(data)
}
