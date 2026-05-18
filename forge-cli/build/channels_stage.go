package build

import (
	"context"
	"fmt"
	"sort"

	clichannels "github.com/initializ/forge/forge-cli/channels"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

// ChannelsStage unions env var names declared by the project's configured
// communication channels into Spec.Requirements.EnvRequired so the generated
// Kubernetes secrets and deployment manifests include them alongside skill
// env vars.
//
// The canonical source is the per-channel YAML (workDir/<channel>-config.yaml)
// — every setting key ending in "_env" declares an env-var name. Adding a new
// channel adapter that ships its own config template will pick up here with
// no edits to this file.
type ChannelsStage struct{}

func (s *ChannelsStage) Name() string { return "channel-env-vars" }

func (s *ChannelsStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	if bc.Config == nil || len(bc.Config.Channels) == 0 {
		return nil
	}

	channelEnv, missing, err := clichannels.EnvVarsFromConfig(bc.Opts.WorkDir, bc.Config.Channels)
	if err != nil {
		return fmt.Errorf("reading channel env vars: %w", err)
	}
	for _, name := range missing {
		bc.AddWarning(fmt.Sprintf("channel %q is configured but %s-config.yaml is missing; its env vars will not be included in the generated manifests", name, name))
	}
	if len(channelEnv) == 0 {
		return nil
	}

	if bc.Spec == nil {
		return nil
	}
	if bc.Spec.Requirements == nil {
		bc.Spec.Requirements = &agentspec.AgentRequirements{}
	}

	// Union with existing skill-required env vars, dedup, sort.
	seen := make(map[string]bool, len(bc.Spec.Requirements.EnvRequired)+len(channelEnv))
	merged := make([]string, 0, len(bc.Spec.Requirements.EnvRequired)+len(channelEnv))
	for _, v := range bc.Spec.Requirements.EnvRequired {
		if seen[v] {
			continue
		}
		seen[v] = true
		merged = append(merged, v)
	}
	// Skill-declared optional env vars stay optional even if a channel marks
	// them required — but in practice channel and skill env-var namespaces
	// don't overlap, so this is just defense-in-depth.
	optional := make(map[string]bool, len(bc.Spec.Requirements.EnvOptional))
	for _, v := range bc.Spec.Requirements.EnvOptional {
		optional[v] = true
	}
	for _, v := range channelEnv {
		if seen[v] || optional[v] {
			continue
		}
		seen[v] = true
		merged = append(merged, v)
	}
	sort.Strings(merged)
	bc.Spec.Requirements.EnvRequired = merged

	return nil
}
