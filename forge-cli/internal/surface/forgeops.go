package surface

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/tools"
)

// forgeExecutable resolves the running forge binary so forge-ops tools invoke
// the very build the user launched (not some other `forge` on PATH). Overridable
// in tests via forgeBinOverride.
var forgeBinOverride string

func forgeExecutable() (string, error) {
	if forgeBinOverride != "" {
		return forgeBinOverride, nil
	}
	return os.Executable()
}

// runForge executes `forge <args...>` in dir (relative to base) with a timeout,
// returning combined stdout+stderr (truncated). A non-zero exit is returned as
// output + error so the model sees the failure text, not just "exit 1".
func runForge(ctx context.Context, base, dir string, timeout time.Duration, args ...string) (string, error) {
	bin, err := forgeExecutable()
	if err != nil {
		return "", fmt.Errorf("locating forge binary: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = safeJoin(base, dir)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	out := capText(strings.TrimSpace(buf.String()), maxDocChars)
	if runCtx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("forge %s timed out after %s", strings.Join(args, " "), timeout)
	}
	if runErr != nil {
		return out, fmt.Errorf("forge %s failed: %w", strings.Join(args, " "), runErr)
	}
	if out == "" {
		out = "forge " + strings.Join(args, " ") + " completed."
	}
	return out, nil
}

// safeJoin joins base and a caller-supplied relative dir, refusing to escape
// base (no absolute paths, no ".." traversal). Falls back to base on any issue.
func safeJoin(base, dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return base
	}
	if filepath.IsAbs(dir) {
		return base
	}
	clean := filepath.Clean(dir)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return base
	}
	return filepath.Join(base, clean)
}

// forgeOp is a self-exec forge-ops tool: it builds a forge argv from JSON args
// and runs it. Shared by the native agent registry and the MCP server.
type forgeOp struct {
	name    string
	desc    string
	schema  string
	timeout time.Duration
	base    string
	build   func(raw json.RawMessage) (args []string, dir string, err error)
}

func (o forgeOp) Name() string                 { return o.name }
func (o forgeOp) Description() string          { return o.desc }
func (o forgeOp) Category() tools.Category     { return tools.CategoryDev }
func (o forgeOp) InputSchema() json.RawMessage { return json.RawMessage(o.schema) }

func (o forgeOp) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args, dir, err := o.build(raw)
	if err != nil {
		return "", err
	}
	return runForge(ctx, o.base, dir, o.timeout, args...)
}

// ---- argument shapes (exported field-less structs kept small & testable) ----

type scaffoldArgs struct {
	Name          string   `json:"name"`
	ModelProvider string   `json:"model_provider"`
	Tools         []string `json:"tools,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	Channels      []string `json:"channels,omitempty"`
	FromSkillDir  string   `json:"from_skill_dir,omitempty"`
	Force         bool     `json:"force,omitempty"`
}

type importSkillArgs struct {
	Folder         string `json:"folder"`
	Dir            string `json:"dir,omitempty"`
	WriteForgeMeta bool   `json:"write_forge_meta,omitempty"`
}

type dirArg struct {
	Dir string `json:"dir,omitempty"`
}

type channelArgs struct {
	Channel string `json:"channel"`
	Dir     string `json:"dir,omitempty"`
}

type skillArgs struct {
	Name string `json:"name"`
	Dir  string `json:"dir,omitempty"`
}

// buildScaffoldArgv is the pure argv builder for forge_scaffold (unit-tested).
func buildScaffoldArgv(raw json.RawMessage) ([]string, string, error) {
	var a scaffoldArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, "", fmt.Errorf("forge_scaffold: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return nil, "", fmt.Errorf("forge_scaffold: name is required")
	}
	if strings.TrimSpace(a.ModelProvider) == "" {
		return nil, "", fmt.Errorf("forge_scaffold: model_provider is required (openai, anthropic, bedrock, gemini, ollama)")
	}
	args := []string{"init", a.Name, "--non-interactive", "--model-provider", a.ModelProvider}
	if len(a.Tools) > 0 {
		args = append(args, "--tools", strings.Join(a.Tools, ","))
	}
	if len(a.Skills) > 0 {
		args = append(args, "--skills", strings.Join(a.Skills, ","))
	}
	if len(a.Channels) > 0 {
		args = append(args, "--channels", strings.Join(a.Channels, ","))
	}
	if strings.TrimSpace(a.FromSkillDir) != "" {
		args = append(args, "--from-skill-dir", a.FromSkillDir)
	}
	if a.Force {
		args = append(args, "--force")
	}
	return args, "", nil // init creates ./<name>; it runs in base
}

// buildImportSkillArgv is the pure argv builder for forge_import_skill.
func buildImportSkillArgv(raw json.RawMessage) ([]string, string, error) {
	var a importSkillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, "", fmt.Errorf("forge_import_skill: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Folder) == "" {
		return nil, "", fmt.Errorf("forge_import_skill: folder is required (path to a skill folder or SKILL.md)")
	}
	args := []string{"skills", "import", a.Folder}
	if a.WriteForgeMeta {
		args = append(args, "--write-forge-meta")
	}
	return args, a.Dir, nil
}

func buildChannelArgv(raw json.RawMessage) ([]string, string, error) {
	var a channelArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, "", fmt.Errorf("forge_add_channel: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Channel) == "" {
		return nil, "", fmt.Errorf("forge_add_channel: channel is required (slack, telegram, msteams, whatsapp)")
	}
	return []string{"channel", "add", a.Channel}, a.Dir, nil
}

func buildSkillArgv(raw json.RawMessage) ([]string, string, error) {
	var a skillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, "", fmt.Errorf("forge_add_skill: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return nil, "", fmt.Errorf("forge_add_skill: name is required")
	}
	return []string{"skills", "add", a.Name}, a.Dir, nil
}

// dirOnly builds a no-flag subcommand that optionally runs in a subdir.
func dirOnly(tool string, sub ...string) func(json.RawMessage) ([]string, string, error) {
	return func(raw json.RawMessage) ([]string, string, error) {
		var a dirArg
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, "", fmt.Errorf("%s: invalid arguments: %w", tool, err)
			}
		}
		return append([]string{}, sub...), a.Dir, nil
	}
}

const dirSchema = `{"type":"object","properties":{"dir":{"type":"string","description":"Agent project subdirectory to run in (relative to the workspace; default: workspace root)."}}}`

// refusedForgeCmds are forge subcommands the generic passthrough refuses:
// long-running/interactive servers that would hang the tool call (run/serve/ui/
// mcp-serve/try), and state-changing/detaching commands that reach outside the
// workspace — notably `optimizer` (its `start` detaches a daemon and rewrites
// ~/.claude/settings.json, #466). forge_run covers the "does it boot?" check;
// the optimizer is managed via the surface's own chooser, not the passthrough.
var refusedForgeCmds = map[string]bool{
	"run": true, "serve": true, "ui": true, "mcp-serve": true, "try": true, "optimizer": true,
}

// forgeCLIArgs is the argument shape for forge_cli.
type forgeCLIArgs struct {
	Args []string `json:"args"`
	Dir  string   `json:"dir,omitempty"`
}

// buildForgeCLIArgv validates and returns the argv for the generic passthrough
// (unit-tested). It refuses empty and long-running commands.
func buildForgeCLIArgv(raw json.RawMessage) ([]string, string, error) {
	var a forgeCLIArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, "", fmt.Errorf("forge_cli: invalid arguments: %w", err)
	}
	if len(a.Args) == 0 || strings.TrimSpace(a.Args[0]) == "" {
		return nil, "", fmt.Errorf("forge_cli: args is required, e.g. [\"skills\",\"list\"] or [\"validate\",\"--help\"]")
	}
	if refusedForgeCmds[a.Args[0]] {
		return nil, "", fmt.Errorf("forge_cli: %q is not allowed via the passthrough (long-running/interactive or state-changing outside the workspace); use forge_run for a boot check, the surface chooser for the optimizer, or run it in your own shell", a.Args[0])
	}
	return a.Args, a.Dir, nil
}

// forgeRunOp is forge_run: a *bounded smoke boot*. `forge run` is a long-lived
// server, so as a tool we start it for a few seconds, capture the startup logs
// (did it bind? did the agent card load?), then stop it — a "does it boot?"
// check for the build loop, never a hanging call. Real serving is done by the
// human (or the agent's own shell) via `forge run`.
type forgeRunOp struct {
	base    string
	window  time.Duration
	timeout time.Duration
}

func (forgeRunOp) Name() string { return "forge_run" }
func (forgeRunOp) Description() string {
	return "Smoke-boot the agent for a few seconds to verify it starts (runs `forge run`, " +
		"captures startup logs, then stops). Not a long-running server — use your shell/`forge run` for that."
}
func (forgeRunOp) Category() tools.Category { return tools.CategoryDev }
func (forgeRunOp) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"dir":{"type":"string","description":"Agent project subdirectory (default: workspace root)."},"port":{"type":"integer","description":"Port to bind (default 8199)."}}}`)
}

func (o forgeRunOp) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Dir  string `json:"dir,omitempty"`
		Port int    `json:"port,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("forge_run: invalid arguments: %w", err)
		}
	}
	port := a.Port
	if port == 0 {
		port = 8199
	}
	bin, err := forgeExecutable()
	if err != nil {
		return "", fmt.Errorf("locating forge binary: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, o.window)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, "run", "--port", fmt.Sprintf("%d", port))
	cmd.Dir = safeJoin(o.base, a.Dir)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	out := capText(strings.TrimSpace(buf.String()), maxDocChars)

	// Timing out is the EXPECTED path: the server booted and ran for the window.
	if runCtx.Err() == context.DeadlineExceeded {
		if out == "" {
			out = "(no startup output captured)"
		}
		return "Agent booted and served for " + o.window.String() + ", then was stopped. Startup logs:\n\n" + out, nil
	}
	// Exited before the window → it crashed on boot; surface the logs as the failure.
	if runErr != nil {
		return out, fmt.Errorf("forge run exited before the smoke window (boot failure): %w", runErr)
	}
	return out, nil
}

// ForgeOpsTools returns the structured forge-ops tools, all rooted at base.
// Shared by the native agent registry and the forge MCP server.
func ForgeOpsTools(base string) []tools.Tool {
	return []tools.Tool{
		forgeRunOp{base: base, window: 12 * time.Second, timeout: 30 * time.Second},
		forgeOp{
			name:    "forge_scaffold",
			desc:    "Scaffold a new Forge agent project (runs `forge init --non-interactive`). Creates ./<name> with forge.yaml + SKILL.md.",
			timeout: 2 * time.Minute,
			base:    base,
			build:   buildScaffoldArgv,
			schema: `{"type":"object","required":["name","model_provider"],"properties":{
  "name":{"type":"string","description":"Agent name (also the project directory)."},
  "model_provider":{"type":"string","description":"openai, anthropic, bedrock, gemini, ollama, or custom."},
  "tools":{"type":"array","items":{"type":"string"},"description":"Built-in tools to enable (e.g. web_fetch, http_request)."},
  "skills":{"type":"array","items":{"type":"string"},"description":"Registry skills to include."},
  "channels":{"type":"array","items":{"type":"string"},"description":"Channels to wire (slack, telegram, …)."},
  "from_skill_dir":{"type":"string","description":"Path to an existing skill folder (SKILL.md + scripts) to vendor into the new agent — the direct way to turn an external/Anthropic skill into an agent."},
  "force":{"type":"boolean","description":"Overwrite an existing directory."}}}`,
		},
		forgeOp{
			name:    "forge_import_skill",
			desc:    "Import an EXTERNAL skill folder or SKILL.md into the current agent project (runs `forge skills import <folder>`). Use this to bring an Anthropic/third-party skill into an existing agent; it wires egress/env and can infer a forge metadata block.",
			timeout: 90 * time.Second,
			base:    base,
			build:   buildImportSkillArgv,
			schema:  `{"type":"object","required":["folder"],"properties":{"folder":{"type":"string","description":"Path to the skill folder or SKILL.md to import."},"write_forge_meta":{"type":"boolean","description":"Inject inferred requires.bins into a plain SKILL.md that has no metadata.forge block."},"dir":{"type":"string","description":"Agent project subdirectory (default: workspace root)."}}}`,
		},
		forgeOp{
			name:    "forge_validate",
			desc:    "Validate the agent spec and forge.yaml (runs `forge validate`).",
			timeout: 90 * time.Second,
			base:    base,
			build:   dirOnly("forge_validate", "validate"),
			schema:  dirSchema,
		},
		forgeOp{
			name:    "forge_build",
			desc:    "Build the agent container artifact (runs `forge build`).",
			timeout: 8 * time.Minute,
			base:    base,
			build:   dirOnly("forge_build", "build"),
			schema:  dirSchema,
		},
		forgeOp{
			name:    "forge_add_channel",
			desc:    "Add a channel adapter to the agent (runs `forge channel add <channel>`).",
			timeout: 60 * time.Second,
			base:    base,
			build:   buildChannelArgv,
			schema:  `{"type":"object","required":["channel"],"properties":{"channel":{"type":"string","enum":["slack","telegram","msteams","whatsapp"]},"dir":{"type":"string"}}}`,
		},
		forgeOp{
			name:    "forge_add_skill",
			desc:    "Add a registry skill to the agent (runs `forge skills add <name>`).",
			timeout: 60 * time.Second,
			base:    base,
			build:   buildSkillArgv,
			schema:  `{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"dir":{"type":"string"}}}`,
		},
		forgeOp{
			name:    "forge_skills_list",
			desc:    "List skills available/attached (runs `forge skills list`).",
			timeout: 30 * time.Second,
			base:    base,
			build:   dirOnly("forge_skills_list", "skills", "list"),
			schema:  dirSchema,
		},
		forgeOp{
			name:    "forge_mcp_list",
			desc:    "List configured MCP servers (runs `forge mcp list`). MCP servers are added by editing forge.yaml — see forge_docs topic \"mcp\".",
			timeout: 30 * time.Second,
			base:    base,
			build:   dirOnly("forge_mcp_list", "mcp", "list"),
			schema:  dirSchema,
		},
		forgeOp{
			// The generic escape hatch: runs ANY forge subcommand, so a new forge
			// capability is reachable the moment it ships — no new wrapper needed
			// (the specific forge_* tools above are just ergonomic shortcuts). The
			// agent can self-discover flags by running e.g. ["skills","--help"].
			name:    "forge_cli",
			desc:    "Run any forge CLI command as an argv array, e.g. args=[\"skills\",\"list\"] or [\"validate\",\"--help\"]. Use this for any forge capability not covered by the specific forge_* tools; run [\"<command>\",\"--help\"] to discover its flags. Does not support long-running commands (run/serve/ui).",
			timeout: 5 * time.Minute,
			base:    base,
			build:   buildForgeCLIArgv,
			schema:  `{"type":"object","required":["args"],"properties":{"args":{"type":"array","items":{"type":"string"},"description":"The forge subcommand and flags, split into an argv array (no leading \"forge\")."},"dir":{"type":"string","description":"Agent project subdirectory to run in (default: workspace root)."}}}`,
		},
	}
}
