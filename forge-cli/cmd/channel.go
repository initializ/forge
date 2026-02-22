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
	corechannels "github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/slack"
	"github.com/initializ/forge/forge-plugins/channels/telegram"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var channelCmd = &cobra.Command{
	Use:   "channel",
	Short: "Manage agent communication channels",
	Long:  "Add and serve channel adapters (Slack, Telegram) for your agent.",
}

var channelAddCmd = &cobra.Command{
	Use:       "add <slack|telegram>",
	Short:     "Add a channel adapter to the project",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"slack", "telegram"},
	RunE:      runChannelAdd,
}

var channelServeCmd = &cobra.Command{
	Use:       "serve <slack|telegram>",
	Short:     "Run a standalone channel adapter (for container use)",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"slack", "telegram"},
	RunE:      runChannelServe,
}

func init() {
	channelCmd.AddCommand(channelAddCmd)
	channelCmd.AddCommand(channelServeCmd)
}

func runChannelAdd(cmd *cobra.Command, args []string) error {
	adapter := args[0]
	if adapter != "slack" && adapter != "telegram" {
		return fmt.Errorf("unsupported adapter: %s (supported: slack, telegram)", adapter)
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
		f.Close()
		return fmt.Errorf("writing .env: %w", err)
	}
	f.Close()
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
	if adapter != "slack" && adapter != "telegram" {
		return fmt.Errorf("unsupported adapter: %s (supported: slack, telegram)", adapter)
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
	router := channels.NewRouter(agentURL)

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
	default:
		return nil
	}
}

// defaultRegistry returns a pre-loaded channel plugin registry.
func defaultRegistry() *corechannels.Registry {
	r := corechannels.NewRegistry()
	r.Register(slack.New())
	r.Register(telegram.New())
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
		fmt.Println("  2. Enable Event Subscriptions and set the Request URL to")
		fmt.Println("     https://<your-host>:3000/slack/events")
		fmt.Println("  3. Subscribe to bot events: message.channels, message.im")
		fmt.Println("  4. Install the app to your workspace")
		fmt.Println("  5. Copy the Signing Secret and Bot Token into .env")
		fmt.Println("  6. Run: forge run --with slack")
	case "telegram":
		fmt.Println("Telegram setup instructions:")
		fmt.Println("  1. Create a bot via @BotFather on Telegram")
		fmt.Println("  2. Copy the bot token into .env")
		fmt.Println("  3. Run: forge run --with telegram")
		fmt.Println()
		fmt.Println("  For webhook mode (requires public URL):")
		fmt.Println("    Set mode: webhook in telegram-config.yaml")
		fmt.Println("    Set your webhook URL via Telegram Bot API")
	}
	fmt.Println()
	fmt.Println(strings.Repeat("─", 40))
	fmt.Printf("Config: %s-config.yaml\n", adapter)
	fmt.Printf("Test:   forge run --with %s\n", adapter)
}
