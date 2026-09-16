package steps

import (
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tui"
)

// TestProviderStep_ApplyCarriesBedrockRegion pins the TUI wiring for the
// native Bedrock provider (#205): the region collected in the region phase
// flows out to the wizard context (→ model.aws_region), and no API key is
// recorded for bedrock. The select→region→model phase transitions are driven
// by the bubbletea components; this guards the terminal Apply contract those
// phases feed.
func TestProviderStep_ApplyCarriesBedrockRegion(t *testing.T) {
	s := &ProviderStep{
		provider:  "bedrock",
		modelID:   "us.amazon.nova-2-lite-v1:0",
		awsRegion: "us-east-1",
	}
	ctx := &tui.WizardContext{EnvVars: map[string]string{}}
	s.Apply(ctx)

	if ctx.Provider != "bedrock" {
		t.Errorf("provider = %q; want bedrock", ctx.Provider)
	}
	if ctx.AWSRegion != "us-east-1" {
		t.Errorf("aws_region = %q; want us-east-1", ctx.AWSRegion)
	}
	if ctx.ModelName != "us.amazon.nova-2-lite-v1:0" {
		t.Errorf("model = %q", ctx.ModelName)
	}
	if ctx.APIKey != "" {
		t.Errorf("bedrock must not carry an API key, got %q", ctx.APIKey)
	}
	if _, ok := ctx.EnvVars["AWS_ACCESS_KEY_ID"]; ok {
		t.Error("wizard must not write AWS credentials to env vars")
	}
}
