package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/initializ/forge/forge-cli/channels"
	"github.com/initializ/forge/forge-cli/templates"
	"github.com/initializ/forge/forge-core/auth"
	corechannels "github.com/initializ/forge/forge-core/channels"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-plugins/channels/msteams"
	"github.com/initializ/forge/forge-plugins/channels/slack"
	"github.com/initializ/forge/forge-plugins/channels/telegram"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var channelCmd = &cobra.Command{
	Use:   "channel",
	Short: "Manage agent communication channels",
	Long:  "Add and serve channel adapters (Slack, Telegram, MS Teams) for your agent.",
}

var channelAddCmd = &cobra.Command{
	Use:       "add <slack|telegram|msteams>",
	Short:     "Add a channel adapter to the project",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"slack", "telegram", "msteams"},
	RunE:      runChannelAdd,
}

var channelServeCmd = &cobra.Command{
	Use:       "serve <slack|telegram|msteams>",
	Short:     "Run a standalone channel adapter (for container use)",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"slack", "telegram", "msteams"},
	RunE:      runChannelServe,
}

// channelDisableCmd adds a channel name to the user (or system)
// policy file's denied_channels list. The denial applies to every
// agent the user runs on this machine — not just the agent in the
// current working directory. Mirrors the GUI chip toggle.
// See issue #90 / FWS-6 (three-layer model).
var channelDisableCmd = &cobra.Command{
	Use:   "disable <name>",
	Short: "Disable a channel via the user (or system) policy file",
	Long: `Disable a channel adapter by adding it to a policy file's denied_channels list.

By default edits ~/.forge/policy.yaml (user-layer policy: applies to every agent
this user runs on this machine). Pass --system to edit /etc/forge/policy.yaml
(system-layer policy: applies to every user on this machine — sysadmin scope,
warns if not run as root).

Idempotent — disabling an already-denied channel is a no-op. The disable
does NOT modify any agent's forge.yaml; policy lives separately from agent
declaration.`,
	Args:    cobra.ExactArgs(1),
	RunE:    runChannelDisable,
	Aliases: []string{"off"},
}

// channelEnableCmd removes a channel name from the user (or system)
// policy file's denied_channels list. Idempotent. Important: this
// only undoes a disable in the SAME layer — a user-layer enable does
// not lift a system-layer or workspace-layer deny.
var channelEnableCmd = &cobra.Command{
	Use:   "enable <name>",
	Short: "Re-enable a previously disabled channel",
	Long: `Remove a channel adapter from a policy file's denied_channels list.

By default edits ~/.forge/policy.yaml. Pass --system to edit /etc/forge/policy.yaml
(warns if not root).

Idempotent — enabling a channel that isn't denied in the target layer is a
no-op. Important: enabling at the user layer does NOT override a system-layer
or workspace-layer deny. If a sysadmin has denied a channel in
/etc/forge/policy.yaml, no per-user enable can lift it.`,
	Args:    cobra.ExactArgs(1),
	RunE:    runChannelEnable,
	Aliases: []string{"on"},
}

// channelSystemFlag selects the system policy file (/etc/forge/policy.yaml)
// for the disable/enable subcommands. Default false → user layer
// (~/.forge/policy.yaml).
var channelSystemFlag bool

func init() {
	channelDisableCmd.Flags().BoolVar(&channelSystemFlag, "system", false, "edit /etc/forge/policy.yaml (system policy) instead of ~/.forge/policy.yaml (user policy); requires write access (typically root)")
	channelEnableCmd.Flags().BoolVar(&channelSystemFlag, "system", false, "edit /etc/forge/policy.yaml (system policy) instead of ~/.forge/policy.yaml (user policy); requires write access (typically root)")
}

func init() {
	channelCmd.AddCommand(channelAddCmd)
	channelCmd.AddCommand(channelServeCmd)
	channelCmd.AddCommand(channelDisableCmd)
	channelCmd.AddCommand(channelEnableCmd)
}

func runChannelAdd(cmd *cobra.Command, args []string) error {
	adapter := args[0]
	if adapter != "slack" && adapter != "telegram" && adapter != "msteams" {
		return fmt.Errorf("unsupported adapter: %s (supported: slack, telegram, msteams)", adapter)
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// 1. Generate {adapter}-config.yaml
	cfgContent := generateChannelConfig(adapter)
	cfgPath := filepath.Join(wd, adapter+"-config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", cfgPath, err)
	}
	fmt.Printf("Created %s-config.yaml\n", adapter)

	// 2. Append env vars to .env
	envPath := filepath.Join(wd, ".env")
	envContent := generateEnvVars(adapter)
	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening .env: %w", err)
	}
	if _, err := f.WriteString(envContent); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing .env: %w", err)
	}
	_ = f.Close()
	fmt.Println("Updated .env with placeholder variables")

	// 3. Update forge.yaml — add channel to channels list
	forgePath := filepath.Join(wd, "forge.yaml")
	if err := addChannelToForgeYAML(forgePath, adapter); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not update forge.yaml: %v\n", err)
	} else {
		fmt.Printf("Added %q to channels in forge.yaml\n", adapter)
	}

	// 4. Update egress config for channel adapter
	if err := addChannelEgressToForgeYAML(forgePath, adapter); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not update egress in forge.yaml: %v\n", err)
	} else {
		fmt.Printf("Updated egress config for %q channel\n", adapter)
	}

	// 5. Print setup instructions
	printSetupInstructions(adapter)
	return nil
}

func runChannelServe(cmd *cobra.Command, args []string) error {
	adapter := args[0]
	if adapter != "slack" && adapter != "telegram" && adapter != "msteams" {
		return fmt.Errorf("unsupported adapter: %s (supported: slack, telegram, msteams)", adapter)
	}

	// Honor every layer's denied_channels list (issue #90 / FWS-6
	// three-layer). Standalone serve typically runs one container per
	// channel under docker-compose / k8s; each container loads the
	// same policy layers (system from /etc/forge, user from $HOME,
	// workspace from FORGE_PLATFORM_POLICY) and refuses to start if
	// its target is denied at any layer.
	layers, layerErr := security.LoadAllPolicyLayers()
	if layerErr != nil {
		return fmt.Errorf("loading platform policy layers: %w", layerErr)
	}
	if src := security.FirstLayerDenyingChannel(layers, adapter); src != nil {
		audit := coreruntime.NewAuditLogger(os.Stderr)
		audit.EmitChannelDeniedByPolicy(adapter, src.Source, src.Path)
		return fmt.Errorf("channel %q denied by %s policy (%s) — refusing to start", adapter, src.Source, src.Path)
	}

	// Load channel config
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	cfgPath := filepath.Join(wd, adapter+"-config.yaml")
	cfg, err := channels.LoadChannelConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading channel config: %w", err)
	}

	// AGENT_URL is required
	agentURL := os.Getenv("AGENT_URL")
	if agentURL == "" {
		return fmt.Errorf("AGENT_URL environment variable is required")
	}

	// Create plugin
	plugin := createPlugin(adapter)
	if plugin == nil {
		return fmt.Errorf("unknown adapter: %s", adapter)
	}

	if err := plugin.Init(*cfg); err != nil {
		return fmt.Errorf("initialising %s plugin: %w", adapter, err)
	}

	// Create router
	// Load auth token if present for the agent directory.
	var channelToken string
	if wd, err := os.Getwd(); err == nil {
		channelToken, _ = auth.LoadToken(wd)
	}
	router := channels.NewRouter(agentURL, channelToken)

	// Signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down channel adapter...")
		cancel()
	}()

	fmt.Fprintf(os.Stderr, "Starting %s adapter (agent: %s)\n", adapter, agentURL)
	return plugin.Start(ctx, router.Handler())
}

// createPlugin returns a new ChannelPlugin for the named adapter.
func createPlugin(name string) corechannels.ChannelPlugin {
	switch name {
	case "slack":
		return slack.New()
	case "telegram":
		return telegram.New()
	case "msteams":
		return msteams.New()
	default:
		return nil
	}
}

// defaultRegistry returns a pre-loaded channel plugin registry.
func defaultRegistry() *corechannels.Registry {
	r := corechannels.NewRegistry()
	r.Register(slack.New())
	r.Register(telegram.New())
	r.Register(msteams.New())
	return r
}

func generateChannelConfig(adapter string) string {
	content, err := templates.GetInitTemplate(adapter + "-config.yaml.tmpl")
	if err != nil {
		// Fallback for unknown adapters
		return ""
	}
	return content
}

func generateEnvVars(adapter string) string {
	content, err := templates.GetInitTemplate("env-" + adapter + ".tmpl")
	if err != nil {
		return ""
	}
	return content
}

func addChannelToForgeYAML(path, adapter string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading forge.yaml: %w", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing forge.yaml: %w", err)
	}

	// Get or create channels list
	var chList []string
	if existing, ok := doc["channels"]; ok {
		if arr, ok := existing.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					chList = append(chList, s)
				}
			}
		}
	}

	// Check if adapter already in list
	for _, ch := range chList {
		if ch == adapter {
			return nil // already present
		}
	}

	chList = append(chList, adapter)

	// Convert back to []any for YAML marshalling
	chAny := make([]any, len(chList))
	for i, s := range chList {
		chAny[i] = s
	}
	doc["channels"] = chAny

	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshalling forge.yaml: %w", err)
	}

	return os.WriteFile(path, out, 0644)
}

func addChannelEgressToForgeYAML(path, adapter string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading forge.yaml: %w", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing forge.yaml: %w", err)
	}

	// Get or create egress map
	egressRaw, ok := doc["egress"]
	if !ok {
		egressRaw = map[string]any{}
	}
	egressMap, ok := egressRaw.(map[string]any)
	if !ok {
		egressMap = map[string]any{}
	}

	switch adapter {
	case "slack":
		// Add "slack" to egress.capabilities
		var caps []string
		if existing, ok := egressMap["capabilities"]; ok {
			if arr, ok := existing.([]any); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						caps = append(caps, s)
					}
				}
			}
		}
		// Check if already present
		for _, c := range caps {
			if c == "slack" {
				return nil // already present
			}
		}
		caps = append(caps, "slack")
		capsAny := make([]any, len(caps))
		for i, s := range caps {
			capsAny[i] = s
		}
		egressMap["capabilities"] = capsAny

	case "telegram":
		// Add "api.telegram.org" to egress.allowed_domains
		var domains []string
		if existing, ok := egressMap["allowed_domains"]; ok {
			if arr, ok := existing.([]any); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						domains = append(domains, s)
					}
				}
			}
		}
		// Check if already present
		for _, d := range domains {
			if d == "api.telegram.org" {
				return nil // already present
			}
		}
		domains = append(domains, "api.telegram.org")
		domainsAny := make([]any, len(domains))
		for i, s := range domains {
			domainsAny[i] = s
		}
		egressMap["allowed_domains"] = domainsAny

	case "msteams":
		// Add "msteams" to egress.capabilities (same pattern as slack).
		// The capability resolves to graph.microsoft.com + login.microsoftonline.com
		// via DefaultCapabilityBundles in forge-core/security/capabilities.go.
		var caps []string
		if existing, ok := egressMap["capabilities"]; ok {
			if arr, ok := existing.([]any); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						caps = append(caps, s)
					}
				}
			}
		}
		for _, c := range caps {
			if c == "msteams" {
				return nil // already present
			}
		}
		caps = append(caps, "msteams")
		capsAny := make([]any, len(caps))
		for i, s := range caps {
			capsAny[i] = s
		}
		egressMap["capabilities"] = capsAny
	}

	doc["egress"] = egressMap

	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshalling forge.yaml: %w", err)
	}

	return os.WriteFile(path, out, 0644)
}

func printSetupInstructions(adapter string) {
	fmt.Println()
	switch adapter {
	case "slack":
		fmt.Println("Slack setup instructions:")
		fmt.Println("  1. Create a Slack App at https://api.slack.com/apps")
		fmt.Println("  2. Enable Socket Mode in your app settings")
		fmt.Println("  3. Generate an app-level token with connections:write scope")
		fmt.Println("  4. Subscribe to bot events: message.channels, message.im")
		fmt.Println("  5. Install the app to your workspace")
		fmt.Println("  6. Add bot scopes: chat:write, app_mentions:read, channels:history,")
		fmt.Println("     im:history, files:write, reactions:write")
		fmt.Println("  7. Install the app and copy the Bot Token (xoxb-...) into .env")
		fmt.Println("  8. Copy the App Token (xapp-...) into .env")
		fmt.Println("  9. Run: forge run --with slack")
	case "telegram":
		fmt.Println("Telegram setup instructions:")
		fmt.Println("  1. Create a bot via @BotFather on Telegram")
		fmt.Println("  2. Copy the bot token into .env")
		fmt.Println("  3. Run: forge run --with telegram")
		fmt.Println()
		fmt.Println("  For webhook mode (requires public URL):")
		fmt.Println("    Set mode: webhook in telegram-config.yaml")
		fmt.Println("    Set your webhook URL via Telegram Bot API")
	case "msteams":
		fmt.Println("Microsoft Teams setup instructions:")
		fmt.Println("  1. Register an Entra ID app at https://entra.microsoft.com")
		fmt.Println("  2. Add delegated API permissions: Chat.Read, Chat.ReadWrite, User.Read")
		fmt.Println("  3. (Optional) Grant admin consent if your tenant requires it")
		fmt.Println("  4. Create a client secret under \"Certificates & secrets\"")
		fmt.Println("  5. Capture a refresh token via the device-code flow — see")
		fmt.Println("     docs/channels/msteams.md for the exact curl invocation")
		fmt.Println("  6. Fill MSTEAMS_TENANT_ID, MSTEAMS_CLIENT_ID, MSTEAMS_CLIENT_SECRET,")
		fmt.Println("     and MSTEAMS_REFRESH_TOKEN in .env")
		fmt.Println("  7. Run: forge run --with msteams")
		fmt.Println()
		fmt.Println("  This adapter is outbound-only — no public endpoint required.")
		fmt.Println("  Default poll cadence is 5s (configurable in msteams-config.yaml).")
	}
	fmt.Println()
	fmt.Println(strings.Repeat("─", 40))
	fmt.Printf("Config: %s-config.yaml\n", adapter)
	fmt.Printf("Test:   forge run --with %s\n", adapter)
}

// runChannelDisable handles `forge channel disable <name>`. Adds the
// name to the target policy file's denied_channels list. Default
// target is ~/.forge/policy.yaml (user layer); --system targets
// /etc/forge/policy.yaml (sysadmin layer). Idempotent — disabling an
// already-denied channel reports "already disabled" and returns 0.
// See issue #90 / FWS-6.
func runChannelDisable(_ *cobra.Command, args []string) error {
	name := args[0]
	path, layerLabel, err := resolveChannelPolicyTarget()
	if err != nil {
		return err
	}
	added, err := mutateDeniedChannelsInPolicy(path, name, true)
	if err != nil {
		return err
	}
	if !added {
		fmt.Printf("Channel %q is already denied in %s policy (%s).\n", name, layerLabel, path)
		return nil
	}
	fmt.Printf("Disabled channel %q via %s policy (%s). The adapter is skipped on next 'forge run'.\n", name, layerLabel, path)
	return nil
}

// runChannelEnable handles `forge channel enable <name>`. Removes the
// name from the target policy file's denied_channels list.
// Idempotent. Important: a user-layer enable does NOT override a
// system-layer or workspace-layer deny — the runtime unions all
// loaded layers, so the channel stays denied if any other layer
// names it.
func runChannelEnable(_ *cobra.Command, args []string) error {
	name := args[0]
	path, layerLabel, err := resolveChannelPolicyTarget()
	if err != nil {
		return err
	}
	removed, err := mutateDeniedChannelsInPolicy(path, name, false)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Printf("Channel %q is not in %s policy denied_channels (%s) — nothing to enable.\n", name, layerLabel, path)
		fmt.Printf("If it's still denied at runtime, check the other policy layers (system / workspace).\n")
		return nil
	}
	fmt.Printf("Enabled channel %q in %s policy (%s). Adapter starts on next 'forge run' (subject to other layers).\n", name, layerLabel, path)
	return nil
}

// resolveChannelPolicyTarget returns the policy file path + label
// based on the --system flag. For the system path it warns when
// euid != 0 (the write will likely fail without root); doesn't refuse
// outright because /etc/forge/policy.yaml may be writable by a
// dedicated group on some sysadmin setups.
func resolveChannelPolicyTarget() (path, label string, err error) {
	if channelSystemFlag {
		path = security.SystemPolicyPath()
		label = "system"
		if os.Geteuid() != 0 {
			fmt.Fprintf(os.Stderr, "Note: editing system policy at %s without root — write may fail. Use sudo or omit --system to edit the user policy at ~/.forge/policy.yaml.\n", path)
		}
		return path, label, nil
	}
	path = security.UserPolicyPath()
	if path == "" {
		return "", "", fmt.Errorf("could not determine user home directory; cannot edit user policy. Use --system or set $HOME")
	}
	return path, "user", nil
}

// mutateDeniedChannelsInPolicy reads the policy file (treating
// missing as empty), adds OR removes `name` from denied_channels, and
// writes the file back. Returns true when a change was applied;
// false when the file already reflected the requested state.
//
// Creates the parent directory if missing (~/.forge/ may not exist
// on first use). Uses yaml round-trip via map[string]any so the
// runtime's strict-decoder (KnownFields(true)) still loads the file
// after the mutation.
func mutateDeniedChannelsInPolicy(path, name string, add bool) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", dir, err)
	}

	var doc map[string]any
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return false, fmt.Errorf("parsing %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}

	var list []string
	if existing, ok := doc["denied_channels"]; ok {
		if arr, ok := existing.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					list = append(list, s)
				}
			}
		}
	}

	idx := -1
	for i, c := range list {
		if c == name {
			idx = i
			break
		}
	}
	if add {
		if idx >= 0 {
			return false, nil
		}
		list = append(list, name)
	} else {
		if idx < 0 {
			return false, nil
		}
		list = append(list[:idx], list[idx+1:]...)
	}

	if len(list) == 0 {
		delete(doc, "denied_channels")
	} else {
		out := make([]any, len(list))
		for i, s := range list {
			out[i] = s
		}
		doc["denied_channels"] = out
	}

	// If the document is now empty (no other policy fields), remove
	// the file entirely so a "clean" enable-everything state has no
	// on-disk noise. Operators inspecting ~/.forge can tell at a
	// glance whether any user policy is set.
	if len(doc) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("removing empty policy %s: %w", path, err)
		}
		return true, nil
	}

	marshalled, err := yaml.Marshal(doc)
	if err != nil {
		return false, fmt.Errorf("marshalling policy: %w", err)
	}
	if err := os.WriteFile(path, marshalled, 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}
