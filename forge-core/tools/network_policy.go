package tools

// NetworkPolicy describes network requirements for registered tools.
type NetworkPolicy struct {
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	DenyAll      bool     `json:"deny_all,omitempty"`
}

// GenerateNetworkPolicy scans registered tools and generates a network policy.
func GenerateNetworkPolicy(reg *Registry) NetworkPolicy {
	policy := NetworkPolicy{}
	hasNetworkTool := false

	for _, name := range reg.List() {
		t := reg.Get(name)
		if t == nil {
			continue
		}

		switch t.Name() {
		case "http_request", "webhook_call", "mcp_call", "openapi_call":
			hasNetworkTool = true
		case "web_search":
			hasNetworkTool = true
		}
	}

	if !hasNetworkTool {
		policy.DenyAll = true
	}

	return policy
}
