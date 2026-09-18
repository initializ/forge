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
	inCI := os.Getenv("CI") != ""
	for _, c := range cases {
		src, err := os.ReadFile(c.src)
		if err != nil {
			// A vacuous skip would let drift pass silently. In CI the source must be
			// present, so fail there; locally (e.g. isolated module build) skip.
			if inCI {
				t.Fatalf("source %s not reachable in CI: %v", c.src, err)
			}
			t.Skipf("source %s not reachable: %v", c.src, err)
		}
		if string(src) != c.embedded {
			t.Errorf("%s has drifted from the embedded copy — run `make sync-knowledge`", c.src)
		}
	}
}
