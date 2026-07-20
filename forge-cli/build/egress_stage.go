package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/security"
)

// EgressStage resolves egress configuration and generates allowlist artifacts.
type EgressStage struct{}

func (s *EgressStage) Name() string { return "resolve-egress" }

func (s *EgressStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	cfg := bc.Config.Egress

	// No-op if no egress config
	if cfg.Profile == "" && cfg.Mode == "" {
		return nil
	}

	// Collect tool names for domain inference
	var toolNames []string
	if bc.Spec != nil {
		for _, t := range bc.Spec.Tools {
			toolNames = append(toolNames, t.Name)
		}
	}

	// Merge auth + MCP + OTel collector domains into the explicit
	// allowlist BEFORE resolving. Without this an OIDC issuer, MCP
	// server URL, or OTLP collector configured in forge.yaml would be
	// silently blocked at runtime — spans would accumulate in the
	// BatchSpanProcessor queue and drop on shutdown timeout, leaving
	// the operator with an inexplicably empty trace backend. Phase 6
	// of OTel Tracing v1 (#107 / #108) closes the loop: "tracing on in
	// forge.yaml" implies "tracing reaches the backend."
	allowed := append([]string{}, cfg.AllowedDomains...)
	allowed = append(allowed, security.AuthDomains(bc.Config.Auth)...)
	allowed = append(allowed, security.MCPDomains(bc.Config.MCP)...)
	allowed = append(allowed, security.OTelDomain(bc.Config.Observability.Tracing)...)
	// Issue #139 — auto-merge LLM provider base URLs declared on
	// model.base_url (and on each fallback). Without this an agent
	// configured against an OpenAI-compatible provider (Together.ai,
	// OpenRouter, Groq, ...) ships a NetworkPolicy that blocks the
	// provider's hostname, and the deployed agent's LLM calls 401 /
	// time out depending on which side notices first.
	allowed = append(allowed, security.LLMProviderDomains(bc.Config)...)

	resolved, err := security.Resolve(cfg.Profile, cfg.Mode, allowed, toolNames, cfg.Capabilities, nil)
	if err != nil {
		return fmt.Errorf("resolving egress: %w", err)
	}

	bc.EgressResolved = resolved

	// Set egress fields on spec
	if bc.Spec != nil {
		bc.Spec.EgressProfile = string(resolved.Profile)
		bc.Spec.EgressMode = string(resolved.Mode)
	}

	// Write egress_allowlist.json
	data, err := security.GenerateAllowlistJSON(resolved)
	if err != nil {
		return fmt.Errorf("generating egress allowlist: %w", err)
	}

	compiledDir := filepath.Join(bc.Opts.OutputDir, "compiled")
	if err := os.MkdirAll(compiledDir, 0755); err != nil {
		return fmt.Errorf("creating compiled directory: %w", err)
	}

	outPath := filepath.Join(compiledDir, "egress_allowlist.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing egress_allowlist.json: %w", err)
	}

	bc.AddFile("compiled/egress_allowlist.json", outPath)
	return nil
}
