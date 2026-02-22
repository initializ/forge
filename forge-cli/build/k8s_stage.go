package build

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/initializ/forge/forge-cli/templates"
	"github.com/initializ/forge/forge-core/compiler"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/security"
)

// K8sStage generates Kubernetes deployment and service manifests.
type K8sStage struct{}

func (s *K8sStage) Name() string { return "generate-k8s-manifests" }

func (s *K8sStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	k8sDir := filepath.Join(bc.Opts.OutputDir, "k8s")
	if err := os.MkdirAll(k8sDir, 0755); err != nil {
		return fmt.Errorf("creating k8s directory: %w", err)
	}

	data := compiler.BuildTemplateDataFromContext(bc.Spec, bc)

	manifests := []struct {
		tmplFile string
		outFile  string
		optional bool
	}{
		{"deployment.yaml.tmpl", "deployment.yaml", false},
		{"service.yaml.tmpl", "service.yaml", false},
		{"network-policy.yaml.tmpl", "network-policy.yaml", true},
		{"secrets.yaml.tmpl", "secrets.yaml", true},
	}

	for _, m := range manifests {
		// Special handling for network-policy: use egress resolver if available
		if m.outFile == "network-policy.yaml" && bc.EgressResolved != nil {
			egressCfg, ok := bc.EgressResolved.(*security.EgressConfig)
			if ok {
				policyData, err := security.GenerateK8sNetworkPolicy(bc.Spec.AgentID, egressCfg)
				if err != nil {
					return fmt.Errorf("generating network policy from egress config: %w", err)
				}
				outPath := filepath.Join(k8sDir, m.outFile)
				if err := os.WriteFile(outPath, policyData, 0644); err != nil {
					return fmt.Errorf("writing %s: %w", m.outFile, err)
				}
				bc.AddFile(filepath.Join("k8s", m.outFile), outPath)
				continue
			}
		}

		tmplData, err := templates.FS.ReadFile(m.tmplFile)
		if err != nil {
			if m.optional {
				continue
			}
			return fmt.Errorf("reading template %s: %w", m.tmplFile, err)
		}

		tmpl, err := template.New(m.tmplFile).Parse(string(tmplData))
		if err != nil {
			return fmt.Errorf("parsing template %s: %w", m.tmplFile, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			if m.optional {
				continue
			}
			return fmt.Errorf("rendering %s: %w", m.tmplFile, err)
		}

		outPath := filepath.Join(k8sDir, m.outFile)
		if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", m.outFile, err)
		}

		bc.AddFile(filepath.Join("k8s", m.outFile), outPath)
	}

	return nil
}
