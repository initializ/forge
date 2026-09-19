package surface

import "github.com/initializ/forge/forge-core/tools"

// MCPToolset is the toolset exposed to Claude Code over the single forge MCP
// server: knowledge retrieval (forge_docs), the structured forge-ops, and the
// initializ platform tool (generate initializ-deploy.yaml — generation only, not
// deployment). When the optimizer is enabled it runs with in-band context
// expansion, so no separate context_expand MCP server is added — this stays the
// one-and-only forge MCP server.
func MCPToolset(workDir string) []tools.Tool {
	set := append([]tools.Tool{ForgeDocsTool{}}, ForgeOpsTools(workDir)...)
	return append(set, InitializTools(workDir)...)
}
