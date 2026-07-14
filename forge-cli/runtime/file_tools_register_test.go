package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

// TestRegisterGeneralFileTools pins the #268 registration decision: a general
// agent gets the file read/write/edit/patch builtins; when the code-agent
// skill is active they are skipped (its code_agent_* tools are the file
// surface — skill tools win, no double surface).
func TestRegisterGeneralFileTools(t *testing.T) {
	r := &Runner{logger: nopLogger{}}
	fileToolNames := []string{"file_read", "file_write", "file_edit", "file_patch"}

	t.Run("general agent registers all four", func(t *testing.T) {
		reg := tools.NewRegistry()
		r.registerGeneralFileTools(reg, t.TempDir(), false /* codeAgentActive */)
		for _, name := range fileToolNames {
			if reg.Get(name) == nil {
				t.Errorf("general agent should have %q registered", name)
			}
		}
	})

	t.Run("code-agent active skips them", func(t *testing.T) {
		reg := tools.NewRegistry()
		r.registerGeneralFileTools(reg, t.TempDir(), true /* codeAgentActive */)
		for _, name := range fileToolNames {
			if reg.Get(name) != nil {
				t.Errorf("code-agent agent should NOT register general %q (code_agent_* wins)", name)
			}
		}
	})
}
