package analyzer

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadPolicyFromFile reads a YAML SecurityPolicy from path. Unspecified fields
// take their zero value, which means no override is applied — a minimal policy
// file can omit any rule it doesn't intend to change.
func LoadPolicyFromFile(path string) (SecurityPolicy, error) {
	var p SecurityPolicy
	data, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("reading policy file %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parsing policy file %q: %w", path, err)
	}
	return p, nil
}
