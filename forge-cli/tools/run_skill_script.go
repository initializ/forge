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
	socksURL string
	envVars  []string
	timeout  time.Duration
}

// NewRunSkillScriptTool constructs the tool rooted at the agent working
// directory, wired to the egress proxy and the skill env passthrough.
// socksURL is the raw-TCP SOCKS5 egress URL (empty when unconfigured).
func NewRunSkillScriptTool(workDir, proxyURL, socksURL string, envVars []string) *RunSkillScriptTool {
	return &RunSkillScriptTool{
		workDir:  workDir,
		proxyURL: proxyURL,
		socksURL: socksURL,
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
		return jsonError("skill and path are required"), nil
	}

	// args, when present, must be a JSON object — it becomes the script's
	// $1 and the script does json.loads(...) expecting a dict.
	jsonArgs := "{}"
	if len(input.Args) > 0 && string(input.Args) != "null" {
		if trimmed := strings.TrimSpace(string(input.Args)); trimmed == "" || trimmed[0] != '{' {
			return jsonError("args must be a JSON object"), nil
		}
		jsonArgs = string(input.Args)
	}

	dir, ok := builtins.SkillDir(t.workDir, input.Skill)
	if !ok {
		return jsonError(fmt.Sprintf("skill %q not found", input.Skill)), nil
	}
	full, err := builtins.SafeSkillJoin(dir, input.Path)
	if err != nil {
		return jsonError("invalid path (must stay within the skill directory)"), nil
	}
	if fi, statErr := os.Stat(full); statErr != nil || fi.IsDir() {
		return jsonError(fmt.Sprintf("script %q not found in skill %q", input.Path, input.Skill)), nil
	}

	interp, ierr := interpreterForScript(input.Path)
	if ierr != nil {
		return jsonError(ierr.Error()), nil
	}

	// CWD = the skill dir so `input.Path` (relative) and the script's own
	// relative references resolve there. Same subprocess posture as
	// cli_execute / skill scripts (egress proxy + env passthrough).
	exec := &SkillCommandExecutor{
		Timeout:  t.timeout,
		WorkDir:  dir,
		EnvVars:  t.envVars,
		ProxyURL: t.proxyURL,
		SOCKSURL: t.socksURL,
	}
	out, runErr := exec.Run(ctx, interp, []string{input.Path, jsonArgs}, nil)
	if runErr != nil {
		// Build via json.Marshal, not fmt %q: script output can carry raw
		// bytes / invalid UTF-8 that %q would emit as \xNN escapes, which
		// are not valid JSON and break the tool-result parser. Marshal
		// replaces invalid UTF-8 with U+FFFD and escapes correctly.
		return jsonObj(map[string]any{"error": runErr.Error(), "output": truncate(out, 8192)}), nil
	}
	return out, nil
}

// jsonObj marshals m to a JSON string, falling back to a static error
// string if marshaling somehow fails.
func jsonObj(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return `{"error": "internal: could not encode result"}`
	}
	return string(b)
}

// jsonError builds a well-formed JSON error object for the given message.
func jsonError(msg string) string { return jsonObj(map[string]any{"error": msg}) }

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
