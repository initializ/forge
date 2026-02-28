package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var secretLocal bool

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage encrypted secrets",
	Long:  "Store, retrieve, and manage secrets in the encrypted secrets file.",
}

var secretSetCmd = &cobra.Command{
	Use:   "set <KEY> [VALUE]",
	Short: "Set a secret (prompts for value if omitted)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		var value string
		if len(args) == 2 {
			value = args[1]
		} else {
			fmt.Fprintf(os.Stderr, "Enter value for %s: ", key)
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			if err != nil {
				return fmt.Errorf("reading value: %w", err)
			}
			fmt.Fprintln(os.Stderr) // newline after hidden input
			value = string(raw)
		}

		p, err := buildEncryptedProvider()
		if err != nil {
			return err
		}
		if err := p.Set(key, value); err != nil {
			return fmt.Errorf("setting secret: %w", err)
		}

		fmt.Printf("Secret %q stored in %s\n", key, secretsPathForDisplay())
		return nil
	},
}

var secretGetCmd = &cobra.Command{
	Use:   "get <KEY>",
	Short: "Get a secret value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		chain, err := buildChainProviderFromDefaults()
		if err != nil {
			return err
		}

		val, source, err := chain.GetWithSource(key)
		if err != nil {
			if secrets.IsNotFound(err) {
				return fmt.Errorf("secret %q not found in any provider", key)
			}
			return err
		}

		fmt.Printf("%s (from %s)\n", val, source)
		return nil
	},
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secret keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		chain, err := buildChainProviderFromDefaults()
		if err != nil {
			return err
		}

		keys, err := chain.List()
		if err != nil {
			return fmt.Errorf("listing secrets: %w", err)
		}

		if len(keys) == 0 {
			fmt.Println("No secrets found.")
			return nil
		}

		for _, k := range keys {
			fmt.Println(k)
		}
		return nil
	},
}

var secretDeleteCmd = &cobra.Command{
	Use:   "delete <KEY>",
	Short: "Delete a secret from the encrypted file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		p, err := buildEncryptedProvider()
		if err != nil {
			return err
		}

		if err := p.Delete(key); err != nil {
			if secrets.IsNotFound(err) {
				return fmt.Errorf("secret %q not found in encrypted file", key)
			}
			return fmt.Errorf("deleting secret: %w", err)
		}

		fmt.Printf("Secret %q deleted.\n", key)
		return nil
	},
}

func init() {
	secretCmd.PersistentFlags().BoolVar(&secretLocal, "local", false, "operate on agent-local secrets (<cwd>/.forge/secrets.enc)")
	secretCmd.AddCommand(secretSetCmd)
	secretCmd.AddCommand(secretGetCmd)
	secretCmd.AddCommand(secretListCmd)
	secretCmd.AddCommand(secretDeleteCmd)
}

// localSecretsPath returns the path for agent-local secrets in the current directory.
func localSecretsPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join(".forge", "secrets.enc")
	}
	return filepath.Join(wd, ".forge", "secrets.enc")
}

// defaultSecretsPath returns the default path for the encrypted secrets file.
func defaultSecretsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".forge", "secrets.enc")
	}
	return filepath.Join(home, ".forge", "secrets.enc")
}

// secretsPathForDisplay returns the path being operated on for user-facing messages.
func secretsPathForDisplay() string {
	if secretLocal {
		return localSecretsPath()
	}
	return defaultSecretsPath()
}

// resolvePassphrase returns the passphrase from FORGE_PASSPHRASE env or terminal prompt.
func resolvePassphrase() (string, error) {
	if p := os.Getenv("FORGE_PASSPHRASE"); p != "" {
		return p, nil
	}

	fmt.Fprint(os.Stderr, "Passphrase: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	return strings.TrimSpace(string(raw)), nil
}

// buildEncryptedProvider builds an EncryptedFileProvider using defaults or config.
func buildEncryptedProvider() (*secrets.EncryptedFileProvider, error) {
	var path string
	if secretLocal {
		path = localSecretsPath()
	} else {
		path = defaultSecretsPath()

		// Try loading config to get custom path
		cfgPath := cfgFile
		if !filepath.IsAbs(cfgPath) {
			wd, _ := os.Getwd()
			cfgPath = filepath.Join(wd, cfgPath)
		}
		if data, err := os.ReadFile(cfgPath); err == nil {
			if cfg, err := parseSecretsPath(data); err == nil && cfg != "" {
				path = cfg
			}
		}
	}

	return secrets.NewEncryptedFileProvider(path, resolvePassphrase), nil
}

// parseSecretsPath extracts secrets.path from raw YAML config bytes.
func parseSecretsPath(data []byte) (string, error) {
	// Minimal parse to avoid importing config package
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "path:") && len(line) > 5 {
			return strings.TrimSpace(line[5:]), nil
		}
	}
	return "", nil
}

// buildChainProviderFromDefaults builds a ChainProvider with encrypted-file + env.
func buildChainProviderFromDefaults() (*secrets.ChainProvider, error) {
	enc, err := buildEncryptedProvider()
	if err != nil {
		return nil, err
	}
	env := secrets.NewEnvProvider("")
	return secrets.NewChainProvider(enc, env), nil
}
