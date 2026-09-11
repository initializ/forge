package cmd

import (
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Regression: the WhatsApp egress bundle introduced the first wildcard domain
// in DefaultCapabilityBundles. Rendered unquoted, its leading "*" is a YAML
// alias indicator, and `forge init` failed with "did not find expected
// alphabetic or numeric character" on the generated forge.yaml.
func TestInitTemplateFuncs_QuotesWildcardDomain(t *testing.T) {
	if got := yamlScalar("*.whatsapp.net"); got != `"*.whatsapp.net"` {
		t.Errorf("yamlScalar(%q) = %q, want it quoted", "*.whatsapp.net", got)
	}
}

// Ordinary domains must stay unquoted so existing generated configs keep
// their current formatting.
func TestInitTemplateFuncs_LeavesPlainDomainsAlone(t *testing.T) {
	for _, s := range []string{"api.openai.com", "web.whatsapp.com", "api.telegram.org", "slack", "web_search"} {
		if got := yamlScalar(s); got != s {
			t.Errorf("yamlScalar(%q) = %q, want it unchanged", s, got)
		}
	}
}

// The template must actually have the helper registered — a missing FuncMap
// entry fails at Parse, which is exactly the wiring this guards.
func TestInitTemplateFuncs_RegisteredOnTemplate(t *testing.T) {
	tmpl, err := template.New("t").Funcs(initTemplateFuncs).Parse(`{{yamlScalar .}}`)
	if err != nil {
		t.Fatalf("parsing with initTemplateFuncs: %v", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, "*.whatsapp.net"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if b.String() != `"*.whatsapp.net"` {
		t.Errorf("rendered %q, want %q", b.String(), `"*.whatsapp.net"`)
	}
}

// End-to-end shape check: the rendered egress block parses and round-trips
// the wildcard intact.
func TestInitTemplate_EgressBlockWithWildcardParses(t *testing.T) {
	domains := []string{"*.whatsapp.net", "api.openai.com", "web.whatsapp.com"}

	tmpl := template.Must(template.New("egress").Funcs(initTemplateFuncs).Parse(
		"egress:\n  mode: allowlist\n  allowed_domains:\n{{- range .}}\n    - {{yamlScalar .}}\n{{- end}}\n"))

	var b strings.Builder
	if err := tmpl.Execute(&b, domains); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var doc struct {
		Egress struct {
			Mode           string   `yaml:"mode"`
			AllowedDomains []string `yaml:"allowed_domains"`
		} `yaml:"egress"`
	}
	if err := yaml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("rendered egress block does not parse: %v\n%s", err, b.String())
	}
	if len(doc.Egress.AllowedDomains) != len(domains) {
		t.Fatalf("got %d domains, want %d: %v", len(doc.Egress.AllowedDomains), len(domains), doc.Egress.AllowedDomains)
	}
	for i, want := range domains {
		if doc.Egress.AllowedDomains[i] != want {
			t.Errorf("domain %d = %q, want %q", i, doc.Egress.AllowedDomains[i], want)
		}
	}
}
