package surface

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedKnowledgeInSync guards against the embedded knowledge drifting from
// the canonical source. If it fails, run `make sync-knowledge`. Skips when the
// source isn't reachable (e.g. an isolated module build).
func TestEmbeddedKnowledgeInSync(t *testing.T) {
	cases := []struct {
		src      string
		embedded string
	}{
		{filepath.Join("..", "..", "..", ".claude", "skills", "forge.md"), forgeKnowledgeMD},
		{filepath.Join("..", "..", "..", ".claude", "skills", "forge-skill-builder.md"), skillBuilderMD},
	}
	for _, c := range cases {
		src, err := os.ReadFile(c.src)
		if err != nil {
			t.Skipf("source %s not reachable: %v", c.src, err)
		}
		if string(src) != c.embedded {
			t.Errorf("%s has drifted from the embedded copy — run `make sync-knowledge`", c.src)
		}
	}
}
