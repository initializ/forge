package auth

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// UnmarshalSettings decodes a freeform settings map into a typed Config
// struct, honoring `yaml:"..."` tags on the destination. Implemented as a
// yaml.Marshal + yaml.Unmarshal roundtrip so each provider can keep its
// Config strongly typed while the public schema stays map[string]any.
//
// Pass a pointer to your Config struct as `out`.
func UnmarshalSettings(in map[string]any, out any) error {
	if out == nil {
		return fmt.Errorf("auth: UnmarshalSettings: out is nil")
	}
	data, err := yaml.Marshal(in)
	if err != nil {
		return fmt.Errorf("auth: settings marshal: %w", err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("auth: settings unmarshal: %w", err)
	}
	return nil
}
