package steps

import (
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

func TestFilterChannelItems(t *testing.T) {
	items := []components.SingleSelectItem{
		{Value: "none"}, {Value: "slack"}, {Value: "telegram"}, {Value: "msteams"},
	}

	t.Run("empty enabled = no filtering", func(t *testing.T) {
		got := filterChannelItems(items, nil)
		if len(got) != len(items) {
			t.Errorf("empty enabled must keep all %d items, got %d", len(items), len(got))
		}
	})

	t.Run("filters to allowlist and always keeps none", func(t *testing.T) {
		got := filterChannelItems(items, []string{"telegram"})
		vals := map[string]bool{}
		for _, it := range got {
			vals[it.Value] = true
		}
		if !vals["telegram"] {
			t.Error("enabled channel telegram must be offered")
		}
		if !vals["none"] {
			t.Error(`the "none" opt-out must always be offered`)
		}
		if vals["slack"] || vals["msteams"] {
			t.Errorf("non-enabled adapters must be filtered out, got %v", vals)
		}
	})
}
