package tools

import (
	"os"

	coretools "github.com/initializ/forge/forge-core/tools"
)

// DiscoverTools scans the given directory for tool scripts/modules.
func DiscoverTools(dir string) []coretools.DiscoveredTool {
	return coretools.DiscoverToolsFS(os.DirFS(dir))
}
