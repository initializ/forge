package runtime

import (
	"os"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// LoadEnvFile reads a .env file and returns key-value pairs.
// Missing files return an empty map and no error.
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return coreruntime.ParseEnvVars(f)
}
