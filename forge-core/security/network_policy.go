package security

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

const networkPolicyTemplate = `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{.AgentID}}-network
  labels:
    app: {{.AgentID}}
  {{- if .Annotation}}
  annotations:
    ai.initializ.forge/allowed-domains: "{{.Annotation}}"
  {{- end}}
spec:
  podSelector:
    matchLabels:
      app: {{.AgentID}}
  policyTypes:
    - Egress
  {{- if .DenyAll}}
  egress: []
  {{- else}}
  egress:
    - to: []
      ports:
        - protocol: TCP
          port: 443
        - protocol: TCP
          port: 80
  {{- end}}`

type networkPolicyTemplateData struct {
	AgentID    string
	DenyAll    bool
	Annotation string
}

// GenerateK8sNetworkPolicy produces a K8s NetworkPolicy YAML for the given agent and egress config.
func GenerateK8sNetworkPolicy(agentID string, cfg *EgressConfig) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("egress config is nil")
	}

	data := networkPolicyTemplateData{
		AgentID: agentID,
	}

	switch cfg.Mode {
	case ModeDenyAll:
		data.DenyAll = true
	case ModeDevOpen:
		data.DenyAll = false
	case ModeAllowlist:
		data.DenyAll = false
		if len(cfg.AllDomains) > 0 {
			data.Annotation = strings.Join(cfg.AllDomains, ",")
		}
	}

	tmpl, err := template.New("network-policy").Parse(networkPolicyTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing network policy template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("rendering network policy: %w", err)
	}

	return buf.Bytes(), nil
}
