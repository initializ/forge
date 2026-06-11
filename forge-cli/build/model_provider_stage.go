package build

import (
	"context"
	"sort"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/catalog"
	"github.com/initializ/forge/forge-core/pipeline"
)

// ModelProviderStage adds the configured model provider's API-key env var
// (e.g. OPENAI_API_KEY) to Spec.Requirements.EnvOptional, so the generated
// secrets.yaml placeholder and the Deployment's secretKeyRef env entry include
// it. Without this the provider key — declared in the catalog but never
// referenced at build time — never reaches the running agent, and the LLM
// client falls back to a stub.
//
// The key is added as OPTIONAL (not required) because a provider may
// authenticate via OAuth instead of an API key, and local providers (e.g.
// Ollama) need no key at all; an unset optional secret key is simply ignored at
// runtime. When a key is supplied at deploy time it is wired through to the pod.
type ModelProviderStage struct{}

func (s *ModelProviderStage) Name() string { return "model-provider-env" }

func (s *ModelProviderStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if bc.Config == nil || bc.Spec == nil {
		return nil
	}
	p, ok := catalog.ProviderByID(bc.Config.Model.Provider)
	if !ok || p.APIKeyEnvVar == "" {
		// Unknown provider, or one that takes no API key (e.g. local/custom).
		return nil
	}
	key := p.APIKeyEnvVar

	if bc.Spec.Requirements == nil {
		bc.Spec.Requirements = &agentspec.AgentRequirements{}
	}
	// Leave it alone if a skill or channel already declares it (required or
	// optional) — don't duplicate or downgrade an existing requirement.
	for _, v := range bc.Spec.Requirements.EnvRequired {
		if v == key {
			return nil
		}
	}
	for _, v := range bc.Spec.Requirements.EnvOptional {
		if v == key {
			return nil
		}
	}

	opt := append(bc.Spec.Requirements.EnvOptional, key)
	sort.Strings(opt)
	bc.Spec.Requirements.EnvOptional = opt
	return nil
}
