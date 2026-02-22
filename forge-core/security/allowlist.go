package security

import "encoding/json"

// allowlistOutput is the JSON structure for egress_allowlist.json.
type allowlistOutput struct {
	Profile        string   `json:"profile"`
	Mode           string   `json:"mode"`
	AllowedDomains []string `json:"allowed_domains"`
	ToolDomains    []string `json:"tool_domains"`
	AllDomains     []string `json:"all_domains"`
}

// GenerateAllowlistJSON produces the JSON output for egress_allowlist.json.
func GenerateAllowlistJSON(cfg *EgressConfig) ([]byte, error) {
	out := allowlistOutput{
		Profile:        string(cfg.Profile),
		Mode:           string(cfg.Mode),
		AllowedDomains: cfg.AllowedDomains,
		ToolDomains:    cfg.ToolDomains,
		AllDomains:     cfg.AllDomains,
	}
	// Ensure empty arrays instead of null in JSON
	if out.AllowedDomains == nil {
		out.AllowedDomains = []string{}
	}
	if out.ToolDomains == nil {
		out.ToolDomains = []string{}
	}
	if out.AllDomains == nil {
		out.AllDomains = []string{}
	}
	return json.MarshalIndent(out, "", "  ")
}
