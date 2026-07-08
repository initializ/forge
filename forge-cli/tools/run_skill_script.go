package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	coretools "github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/builtins"
)

// RunSkillScriptTool executes a helper script that ships inside a skill's
// own directory — the kind a SKILL.md body references by relative path
// ("run owl/scripts/check.py"). Unlike a `## Tool:` entry (registered as a
// first-class callable tool via registerSkillTools, `.sh`-only), this tool
// resolves an arbitrary script path relative to the skill directory, picks
// the interpreter from the extension (shell / python / javascript), and
// runs it with the skill directory as the working directory so the
// script's own relative references resolve. See issue #251.
//
// Path resolution is confined to the skill directory (no `..` / absolute
// escape) via builtins.SafeSkillJoin. The subprocess inherits the same
// egress proxy + env passthrough as cli_execute and skill scripts.
type RunSkillScriptTool struct {
	workDir  string
	proxyURL string
	envVars  []string
	timeout  time.Duration
}

// NewRunSkillScriptTool constructs the tool rooted at the agent working
// directory, wired to the egress proxy and the skill env passthrough.
func NewRunSkillScriptTool(workDir, proxyURL string, envVars []string) *RunSkillScriptTool {
	return &RunSkillScriptTool{
		workDir:  workDir,
		proxyURL: proxyURL,
		envVars:  envVars,
		timeout:  120 * time.Second,
	}
}

func (t *RunSkillScriptTool) Name() string                 { return "run_skill_script" }
func (t *RunSkillScriptTool) Category() coretools.Category { return coretools.CategoryBuiltin }

func (t *RunSkillScriptTool) Description() string {
	return "Execute a helper script bundled inside a skill's directory (shell .sh, python .py, or javascript .js). " +
		"The path is resolved relative to the skill and the script runs with the skill's directory as its working " +
		"directory, so the script's own relative references resolve. Use for skill instructions like " +
		"'run owl/scripts/check.py'. JSON in 'args' is passed to the script as its first positional argument ($1)."
}

func (t *RunSkillScriptTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"skill": {"type": "string", "description": "Skill name exactly as shown in Available Skills (the identifier before the colon)."},
			"path": {"type": "string", "description": "Script path RELATIVE TO THE SKILL directory (e.g. 'scripts/check.py'). Supported: .sh, .py, .js."},
			"args": {"type": "object", "description": "Optional JSON object passed to the script as its first positional argument ($1)."}
		},
		"required": ["skill", "path"]
	}`)
}

func (t *RunSkillScriptTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Skill string          `json:"skill"`
		Path  string          `json:"path"`
		Args  json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing run_skill_script input: %w", err)
	}
	if input.Skill == "" || input.Path == "" {
		return `{"error": "skill and path are required"}`, nil
	}

	dir, ok := builtins.SkillDir(t.workDir, input.Skill)
	if !ok {
		return fmt.Sprintf(`{"error": "skill %q not found"}`, input.Skill), nil
	}
	full, err := builtins.SafeSkillJoin(dir, input.Path)
	if err != nil {
		return `{"error": "invalid path (must stay within the skill directory)"}`, nil
	}
	if fi, statErr := os.Stat(full); statErr != nil || fi.IsDir() {
		return fmt.Sprintf(`{"error": "script %q not found in skill %q"}`, input.Path, input.Skill), nil
	}

	interp, ierr := interpreterForScript(input.Path)
	if ierr != nil {
		return fmt.Sprintf(`{"error": %q}`, ierr.Error()), nil
	}

	jsonArgs := "{}"
	if len(input.Args) > 0 && string(input.Args) != "null" {
		jsonArgs = string(input.Args)
	}

	// CWD = the skill dir so `input.Path` (relative) and the script's own
	// relative references resolve there. Same subprocess posture as
	// cli_execute / skill scripts (egress proxy + env passthrough).
	exec := &SkillCommandExecutor{
		Timeout:  t.timeout,
		WorkDir:  dir,
		EnvVars:  t.envVars,
		ProxyURL: t.proxyURL,
	}
	out, runErr := exec.Run(ctx, interp, []string{input.Path, jsonArgs}, nil)
	if runErr != nil {
		return fmt.Sprintf(`{"error": %q, "output": %q}`, runErr.Error(), truncate(out, 8192)), nil
	}
	return out, nil
}

// interpreterForScript picks the interpreter for a script by extension.
// TypeScript is intentionally unsupported — `node` can't run raw `.ts`;
// ship a compiled `.js` instead.
func interpreterForScript(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sh", ".bash":
		return "bash", nil
	case ".py":
		return "python3", nil
	case ".js", ".cjs", ".mjs":
		return "node", nil
	default:
		return "", fmt.Errorf("unsupported script type %q (supported: .sh, .py, .js)", filepath.Ext(path))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}
