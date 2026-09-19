package surface

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

// Agent runtime types the initializ platform accepts today. Mirrors
// deployspec.Type* in the initializ CLI. langchain / google-adk are planned.
var initializAgentTypes = []string{"forge", "claude-agent", "strands"}

func isManagedType(t string) bool { return t == "claude-agent" || t == "strands" }

// Detection is the outcome of investigating a project directory to infer which
// initializ agent type a deploy spec should be generated for.
type Detection struct {
	Type     string   // "forge" | "claude-agent" | "strands" | "" (unknown)
	Language string   // "node" | "python" | "" (n/a for forge)
	Evidence []string // human-readable findings (files + matched dependencies)
}

// dependency markers (substring, case-insensitive) that identify a runtime.
var (
	claudeNodeMarkers = []string{"@anthropic-ai/claude-agent-sdk", "@anthropic-ai/claude-code", "@anthropic-ai/sdk", "@initializ/a2a-kit"}
	claudePyMarkers   = []string{"claude-agent-sdk", "claude_agent_sdk", "anthropic"}
	strandsMarkers    = []string{"strands-agents", "strands_agents", "strands"}
)

// DetectAgent investigates dir to infer the initializ agent type: forge.yaml →
// forge; else a claude/strands dependency in package.json (node) or
// requirements.txt / pyproject.toml (python). The result is a recommendation —
// the caller confirms with the user before generating.
func DetectAgent(dir string) Detection {
	d := Detection{}

	// forge.yaml (or .yml) is the definitive forge marker.
	for _, fn := range []string{"forge.yaml", "forge.yml"} {
		if raw, ok := readFileIfExists(filepath.Join(dir, fn)); ok {
			d.Type = "forge"
			ev := fn + " present"
			if id := grepAgentID(raw); id != "" {
				ev += " (agent_id: " + id + ")"
			}
			d.Evidence = append(d.Evidence, ev)
			break
		}
	}

	// Node project: package.json dependencies.
	if raw, ok := readFileIfExists(filepath.Join(dir, "package.json")); ok {
		d.Evidence = append(d.Evidence, "package.json present")
		deps, hasInitializ := parsePackageJSON(raw)
		blob := strings.ToLower(strings.Join(deps, "\n"))
		switch {
		case matchAny(blob, strandsMarkers):
			d.setIfEmpty("strands", "node", &d.Evidence, "package.json → strands dependency")
		case matchAny(blob, claudeNodeMarkers):
			d.setIfEmpty("claude-agent", "node", &d.Evidence, "package.json → Claude Agent SDK dependency")
		case hasInitializ:
			d.setIfEmpty("claude-agent", "node", &d.Evidence, `package.json → "initializ" a2a pointer`)
		}
	}

	// Python project: requirements.txt / pyproject.toml.
	var py strings.Builder
	for _, fn := range []string{"requirements.txt", "pyproject.toml"} {
		if raw, ok := readFileIfExists(filepath.Join(dir, fn)); ok {
			d.Evidence = append(d.Evidence, fn+" present")
			py.WriteString(strings.ToLower(raw))
			py.WriteByte('\n')
		}
	}
	if blob := py.String(); blob != "" {
		switch {
		case matchAny(blob, strandsMarkers):
			d.setIfEmpty("strands", "python", &d.Evidence, "python deps → strands")
		case matchAny(blob, claudePyMarkers):
			d.setIfEmpty("claude-agent", "python", &d.Evidence, "python deps → claude/anthropic")
		}
	}
	return d
}

// setIfEmpty records the type/language/evidence only when a type hasn't already
// been inferred (forge.yaml and earlier markers win), but always logs evidence.
func (d *Detection) setIfEmpty(typ, lang string, ev *[]string, note string) {
	*ev = append(*ev, note)
	if d.Type == "" {
		d.Type = typ
		d.Language = lang
	}
}

func matchAny(haystack string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(haystack, m) {
			return true
		}
	}
	return false
}

func readFileIfExists(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// parsePackageJSON returns the dependency names (deps + devDeps) and whether an
// "initializ" key (the a2a pointer written by `initializ agent init`) is present.
func parsePackageJSON(raw string) (deps []string, hasInitializ bool) {
	var pkg struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
		Initializ       json.RawMessage            `json:"initializ"`
	}
	if json.Unmarshal([]byte(raw), &pkg) != nil {
		return nil, false
	}
	for k := range pkg.Dependencies {
		deps = append(deps, k)
	}
	for k := range pkg.DevDependencies {
		deps = append(deps, k)
	}
	return deps, len(pkg.Initializ) > 0
}

// grepAgentID does a light scan for `agent_id: <value>` in forge.yaml (avoids a
// full config dependency just to surface the name as evidence).
func grepAgentID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "agent_id:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "agent_id:")), `"'`)
		}
	}
	return ""
}

// deployEnvVar is one env entry in the generated initializ-deploy.yaml.
type deployEnvVar struct {
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// deployGenArgs is the input for initializ_deploy_generate.
type deployGenArgs struct {
	Type      string `json:"type"`
	Name      string `json:"name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	ModelName string `json:"model_name,omitempty"`
	Image     string `json:"image,omitempty"`
	Workspace string `json:"workspace,omitempty"`

	// Expose picks the exposure mode for claude-agent/strands: "a2a" (Agent2Agent)
	// or "http" (a plain invoke endpoint). Default "a2a". Not valid for forge —
	// forge agents are always A2A, auto-wired by the platform (never declared).
	Expose         string          `json:"expose,omitempty"`
	A2AAuth        string          `json:"a2a_auth,omitempty"` // "" | bearer | none
	A2AName        string          `json:"a2a_name,omitempty"`
	A2ADescription string          `json:"a2a_description,omitempty"`
	A2APort        int             `json:"a2a_port,omitempty"`
	HTTPPath       string          `json:"http_path,omitempty"`   // required for expose=http
	HTTPMethod     string          `json:"http_method,omitempty"` // default POST
	HTTPInput      json.RawMessage `json:"http_input,omitempty"`  // JSON-Schema object for the request

	Env           []deployEnvVar `json:"env,omitempty"`
	EgressDomains []string       `json:"egress_domains,omitempty"`
	Replicas      int            `json:"replicas,omitempty"`
	Dir           string         `json:"dir,omitempty"`
	Write         *bool          `json:"write,omitempty"` // default true
}

// exposureMode resolves the requested exposure for a managed (non-forge) agent
// to "a2a" or "http", validating consistency. Default is a2a.
func (a deployGenArgs) exposureMode() (string, error) {
	expose := strings.ToLower(strings.TrimSpace(a.Expose))
	hasHTTP := a.HTTPPath != "" || a.HTTPMethod != "" || len(a.HTTPInput) > 0
	hasA2A := a.A2AAuth != "" || a.A2AName != "" || a.A2ADescription != "" || a.A2APort != 0

	switch expose {
	case "a2a":
		if hasHTTP {
			return "", fmt.Errorf("expose: a2a but http_* fields are set — pick one exposure mode")
		}
		return "a2a", nil
	case "http":
		if hasA2A {
			return "", fmt.Errorf("expose: http but a2a_* fields are set — pick one exposure mode")
		}
		return "http", nil
	case "":
		if hasHTTP && hasA2A {
			return "", fmt.Errorf("a2a and http are mutually exclusive — set expose to \"a2a\" or \"http\"")
		}
		if hasHTTP {
			return "http", nil
		}
		return "a2a", nil // default exposure for managed runtimes
	default:
		return "", fmt.Errorf("unsupported expose %q (want \"a2a\" or \"http\")", a.Expose)
	}
}

// yamlScalar renders s as a safe YAML scalar: plain when unambiguous, otherwise
// a double-quoted (JSON) scalar. This keeps free-text values (descriptions, env
// values, image refs) from producing invalid YAML (a plain scalar can't contain
// ": ") or injecting sibling keys via an embedded newline.
func yamlScalar(s string) string {
	if needsYAMLQuote(s) {
		q, _ := json.Marshal(s) // a JSON string is a valid YAML double-quoted scalar
		return string(q)
	}
	return s
}

// needsYAMLQuote reports whether s must be quoted to be a safe plain YAML scalar.
func needsYAMLQuote(s string) bool {
	if s == "" || s != strings.TrimSpace(s) {
		return true
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return true
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") || strings.Contains(s, " #") {
		return true
	}
	switch s[0] { // leading YAML indicator characters
	case '!', '&', '*', '[', ']', '{', '}', '#', '|', '>', '@', '`', '"', '\'', '%', ',', '?', ':', '-', ' ':
		return true
	}
	switch strings.ToLower(s) { // words YAML would parse as non-string
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return true
	}
	// Numeric-looking scalars (e.g. an env value "8080" or "1.5") would parse as a
	// YAML int/float — and then fail a string unmarshal on the platform — so quote
	// them. ParseInt base 0 covers decimal / 0x / 0o / 0b / underscores + sign;
	// ParseFloat covers 1.5 / .5 / 1e3.
	if _, err := strconv.ParseInt(s, 0, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

// GenerateInitializDeploy renders a valid initializ-deploy.yaml for the given
// args, enforcing the same invariants as the platform's deployspec.Validate()
// (cross-checked against initializ cli-next internal/deployspec, 2026-09).
// Returns the rendered YAML (the caller decides whether to write it).
func GenerateInitializDeploy(a deployGenArgs) (string, error) {
	t := strings.TrimSpace(a.Type)
	if t == "" {
		return "", fmt.Errorf("type is required (one of: %s)", strings.Join(initializAgentTypes, ", "))
	}
	forge := t == "forge"
	if !forge && !isManagedType(t) {
		return "", fmt.Errorf("agent type %q is not supported yet (v1 supports: %s; langchain/google-adk are planned)",
			t, strings.Join(initializAgentTypes, ", "))
	}

	name := strings.TrimSpace(a.Name)
	if name != "" && !validAgentSlug(name) {
		return "", fmt.Errorf("name %q must be a DNS-1123 label (a-z, 0-9, -)", name)
	}
	if !forge && name == "" {
		return "", fmt.Errorf("%s agents require agent.name (there is no forge.yaml to default it from)", t)
	}

	provider := strings.TrimSpace(a.Provider)
	auth := strings.ToLower(strings.TrimSpace(a.A2AAuth))
	mode := "" // "a2a" | "http" for managed runtimes
	httpMethod := ""
	if forge {
		// forge agents are ALWAYS A2A — the platform auto-wires it; the spec must
		// not declare provider/exposure.
		if provider != "" {
			return "", fmt.Errorf("model.provider is not set on forge specs — it comes from forge.yaml")
		}
		if strings.TrimSpace(a.Expose) != "" || a.HTTPPath != "" || a.HTTPMethod != "" || len(a.HTTPInput) > 0 ||
			a.A2AAuth != "" || a.A2AName != "" || a.A2ADescription != "" || a.A2APort != 0 {
			return "", fmt.Errorf("forge agents are always A2A (auto-wired by the platform) — don't set expose/a2a/http on a forge spec")
		}
	} else {
		switch provider {
		case "anthropic", "openai":
		case "":
			return "", fmt.Errorf("%s agents require model.provider (anthropic|openai)", t)
		default:
			return "", fmt.Errorf("unsupported model.provider %q (want anthropic|openai)", provider)
		}
		var err error
		if mode, err = a.exposureMode(); err != nil {
			return "", err
		}
		switch auth {
		case "", "bearer", "none":
		default:
			return "", fmt.Errorf("unsupported a2a_auth %q (want \"\", bearer, or none)", a.A2AAuth)
		}
		if mode == "http" {
			if a.HTTPPath == "" {
				return "", fmt.Errorf("expose: http requires http_path (the invoke endpoint, e.g. /invocations)")
			}
			if !strings.HasPrefix(a.HTTPPath, "/") {
				return "", fmt.Errorf("http_path %q must start with '/'", a.HTTPPath)
			}
			// Reject a second-position '/' or '\' (e.g. "//x", "/\x") — not a valid
			// invoke path, and it also placates the open-redirect linter.
			if len(a.HTTPPath) > 1 && (a.HTTPPath[1] == '/' || a.HTTPPath[1] == '\\') {
				return "", fmt.Errorf("http_path %q must not start with '//' or '/\\'", a.HTTPPath)
			}
			httpMethod = strings.ToUpper(strings.TrimSpace(a.HTTPMethod))
			switch httpMethod {
			case "", "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return "", fmt.Errorf("unsupported http_method %q", a.HTTPMethod)
			}
			if httpMethod == "" {
				httpMethod = "POST"
			}
			if len(a.HTTPInput) > 0 && !json.Valid(a.HTTPInput) {
				return "", fmt.Errorf("http_input must be a JSON object (a JSON-Schema for the request body)")
			}
		}
	}
	for _, e := range a.Env {
		if strings.TrimSpace(e.Name) == "" {
			return "", fmt.Errorf("env entry with empty name")
		}
	}

	image := strings.TrimSpace(a.Image)
	if image == "" {
		base := name
		if base == "" {
			base = "your-agent"
		}
		image = "registry.initializ.ai/" + base + ":latest"
	}
	replicas := a.Replicas
	if replicas <= 0 {
		replicas = 1
	}

	var b strings.Builder
	b.WriteString("apiVersion: initializ.ai/v1\nkind: AgentDeploy\n\nagent:\n")
	if name != "" {
		fmt.Fprintf(&b, "  name: %s\n", yamlScalar(name))
	} else {
		b.WriteString("  # name: defaults from forge.yaml agent_id when omitted\n")
	}
	fmt.Fprintf(&b, "  type: %s\n", t)
	if ws := strings.TrimSpace(a.Workspace); ws != "" {
		fmt.Fprintf(&b, "  workspace: %s\n", yamlScalar(ws))
	}
	fmt.Fprintf(&b, "\nimage: %s\n", yamlScalar(image))

	if forge {
		b.WriteString("\nforge:\n  path: ./forge.yaml\n  outputDir: ./.forge-output\n")
	} else {
		b.WriteString("\nmodel:\n")
		fmt.Fprintf(&b, "  provider: %s\n", provider)
		if mn := strings.TrimSpace(a.ModelName); mn != "" {
			fmt.Fprintf(&b, "  name: %s\n", yamlScalar(mn))
		}
		switch mode {
		case "http":
			// Plain-HTTP invoke endpoint (not A2A).
			b.WriteString("\nhttp:\n")
			fmt.Fprintf(&b, "  path: %s\n  method: %s\n", yamlScalar(a.HTTPPath), httpMethod)
			if len(a.HTTPInput) > 0 {
				var compact bytes.Buffer
				if err := json.Compact(&compact, a.HTTPInput); err == nil {
					fmt.Fprintf(&b, "  input: %s\n", compact.String())
				}
			}
		default: // "a2a" — Agent2Agent exposure
			b.WriteString("\na2a:\n  enabled: true\n")
			if a.A2APort != 0 {
				fmt.Fprintf(&b, "  port: %d\n", a.A2APort)
			}
			if n := strings.TrimSpace(a.A2AName); n != "" {
				fmt.Fprintf(&b, "  name: %s\n", yamlScalar(n))
			}
			if d := strings.TrimSpace(a.A2ADescription); d != "" {
				fmt.Fprintf(&b, "  description: %s\n", yamlScalar(d))
			}
			if auth != "" {
				fmt.Fprintf(&b, "  auth: %s\n", auth)
			}
		}
	}

	if len(a.Env) > 0 {
		b.WriteString("\nenv:\n")
		for _, e := range a.Env {
			fmt.Fprintf(&b, "  - name: %s\n", yamlScalar(e.Name))
			if e.Value != "" {
				fmt.Fprintf(&b, "    value: %s\n", yamlScalar(e.Value))
			}
			if e.Secret {
				b.WriteString("    secret: true\n")
			}
			if e.Optional {
				b.WriteString("    optional: true\n")
			}
		}
	}

	mem := "256Mi"
	if !forge {
		mem = "512Mi"
	}
	fmt.Fprintf(&b, "\nresources:\n  replicas: %d\n  requests: {cpu: 250m, memory: %s}\n  limits: {cpu: \"1\", memory: 1Gi}\n", replicas, mem)

	if len(a.EgressDomains) > 0 {
		b.WriteString("\negress:\n  additionalDomains:\n")
		for _, d := range a.EgressDomains {
			fmt.Fprintf(&b, "    - %s\n", yamlScalar(d))
		}
	}

	b.WriteString("\ndeploy:\n  wait: true\n  timeout: 10m\n")
	return b.String(), nil
}

// validAgentSlug reports whether s is a DNS-1123 label (mirrors the initializ CLI).
func validAgentSlug(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i != 0 && i != len(s)-1:
		default:
			return false
		}
	}
	return true
}

// initializDeployTool is the initializ_deploy_generate tool: it renders (and, by
// default, writes) an initializ-deploy.yaml for a chosen agent type. Self-
// contained — it needs no initializ CLI installed.
type initializDeployTool struct{ base string }

func (initializDeployTool) Name() string { return "initializ_deploy_generate" }
func (initializDeployTool) Description() string {
	return "Generate an initializ-deploy.yaml to deploy an agent to the initializ platform " +
		"(generation only — the operator deploys it). Agent types: forge, claude-agent, strands " +
		"(langchain/google-adk planned). forge reads forge.yaml and is ALWAYS A2A (auto-wired — do not " +
		"set expose/a2a/http). claude-agent & strands require name + model.provider and choose ONE " +
		"exposure via `expose`: \"a2a\" (Agent2Agent, the default) or \"http\" (a plain invoke endpoint, " +
		"needs http_path). Writes ./initializ-deploy.yaml by default (write:false returns it only). " +
		"See forge_docs topic \"initializ-deploy\"."
}
func (initializDeployTool) Category() tools.Category { return tools.CategoryDev }
func (initializDeployTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["type"],
  "properties": {
    "type": {"type": "string", "enum": ["forge", "claude-agent", "strands"], "description": "Agent runtime type."},
    "name": {"type": "string", "description": "Agent name (DNS-1123 label). Required for claude-agent/strands; optional for forge (defaults from forge.yaml)."},
    "provider": {"type": "string", "enum": ["anthropic", "openai"], "description": "Managed model provider. Required for claude-agent/strands; must be omitted for forge."},
    "model_name": {"type": "string", "description": "Optional model pin (non-forge)."},
    "image": {"type": "string", "description": "Image ref (default registry.initializ.ai/<name>:latest)."},
    "workspace": {"type": "string", "description": "Workspace id ws_… (optional)."},
    "expose": {"type": "string", "enum": ["a2a", "http"], "description": "Exposure mode for claude-agent/strands: a2a (default) or http (invoke endpoint). Not valid for forge."},
    "a2a_auth": {"type": "string", "enum": ["", "bearer", "none"], "description": "A2A auth mode (expose=a2a)."},
    "a2a_name": {"type": "string", "description": "Agent Card name (expose=a2a; default agent.name)."},
    "a2a_description": {"type": "string", "description": "Agent Card description (expose=a2a)."},
    "a2a_port": {"type": "integer", "description": "A2A server container port (expose=a2a; default 9090)."},
    "http_path": {"type": "string", "description": "Invoke path starting with '/' (required for expose=http, e.g. /invocations)."},
    "http_method": {"type": "string", "enum": ["GET", "POST", "PUT", "PATCH", "DELETE"], "description": "Invoke method (expose=http; default POST)."},
    "http_input": {"type": "object", "description": "JSON-Schema object describing the invoke request body (expose=http; optional)."},
    "env": {"type": "array", "items": {"type": "object", "properties": {"name": {"type": "string"}, "value": {"type": "string"}, "secret": {"type": "boolean"}, "optional": {"type": "boolean"}}}, "description": "Container env; value supports ${VAR} interpolation."},
    "egress_domains": {"type": "array", "items": {"type": "string"}, "description": "Extra egress domains."},
    "replicas": {"type": "integer", "description": "Replica count (default 1)."},
    "dir": {"type": "string", "description": "Directory to write into (default workspace root)."},
    "write": {"type": "boolean", "description": "Write ./initializ-deploy.yaml (default true). false = return the YAML only."}
  }
}`)
}

func (o initializDeployTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a deployGenArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("initializ_deploy_generate: invalid arguments: %w", err)
	}
	yaml, err := GenerateInitializDeploy(a)
	if err != nil {
		return "", fmt.Errorf("initializ_deploy_generate: %w", err)
	}
	write := a.Write == nil || *a.Write
	if !write {
		return yaml, nil
	}
	// safeJoin confines dir to the workspace (filepath.IsLocal barrier); the
	// filename is a constant, so the write target can't escape it.
	path := filepath.Join(safeJoin(o.base, a.Dir), "initializ-deploy.yaml")
	_, existed := readFileIfExists(path)
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil { //nolint:gosec // user-authored deploy manifest, not a secret
		return "", fmt.Errorf("initializ_deploy_generate: writing %s: %w", path, err)
	}
	verb := "Wrote"
	if existed {
		verb = "Replaced existing"
	}
	return verb + " " + path + "\n\n" + yaml, nil
}

// initializDetectTool investigates the project directory to infer the agent
// type BEFORE generating a spec — so the agent can confirm with the user rather
// than guess.
type initializDetectTool struct{ base string }

func (initializDetectTool) Name() string { return "initializ_detect_agent" }
func (initializDetectTool) Description() string {
	return "Investigate the current project to infer which initializ agent type a deploy spec is for: " +
		"forge (forge.yaml present), or claude-agent / strands (a Claude/Strands dependency in " +
		"package.json → node, or requirements.txt / pyproject.toml → python). Call this FIRST when the " +
		"user asks to generate an initializ deploy config, then CONFIRM the detected type (and node/python) " +
		"with the user before calling initializ_deploy_generate."
}
func (initializDetectTool) Category() tools.Category { return tools.CategoryDev }
func (initializDetectTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"dir":{"type":"string","description":"Project subdirectory to investigate (default: workspace root)."}}}`)
}

func (o initializDetectTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Dir string `json:"dir,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("initializ_detect_agent: invalid arguments: %w", err)
		}
	}
	d := DetectAgent(safeJoin(o.base, a.Dir))

	var b strings.Builder
	if d.Type == "" {
		b.WriteString("Detected agent type: UNKNOWN — could not infer it from the project.\n")
	} else if d.Language != "" {
		fmt.Fprintf(&b, "Detected agent type: %s (%s)\n", d.Type, d.Language)
	} else {
		fmt.Fprintf(&b, "Detected agent type: %s\n", d.Type)
	}
	if len(d.Evidence) == 0 {
		b.WriteString("Evidence: none (no forge.yaml, package.json, requirements.txt, or pyproject.toml found)\n")
	} else {
		b.WriteString("Evidence:\n")
		for _, e := range d.Evidence {
			fmt.Fprintf(&b, "  - %s\n", e)
		}
	}
	b.WriteString("\nNext: CONFIRM with the user what to generate for — type (forge, claude-agent, or strands)")
	if d.Type != "forge" {
		b.WriteString(" and language (node or python)")
	}
	b.WriteString(" — then call initializ_deploy_generate. Do not generate without confirming.")
	return b.String(), nil
}

// InitializTools returns the platform tools shared by the MCP toolset (Claude
// Code) and the native agent registry. Scope is deliberately GENERATION ONLY —
// investigate (initializ_detect_agent) then generate (initializ_deploy_generate)
// the initializ-deploy.yaml; deploying is done by the operator via the initializ
// CLI in their own shell/CI (see the forge_docs "initializ-deploy" topic).
func InitializTools(base string) []tools.Tool {
	return []tools.Tool{
		initializDetectTool{base: base},
		initializDeployTool{base: base},
	}
}
