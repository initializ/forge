package cmd

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-skills/trust"
	"github.com/spf13/cobra"
)

var keyName string

var keyCmd = &cobra.Command{
	Use:   "key",
	Short: "Manage signing keys",
	Long:  "Generate, trust, and list Ed25519 signing keys for build artifact verification.",
}

var keyGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate an Ed25519 signing keypair",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("getting home directory: %w", err)
		}

		forgeDir := filepath.Join(home, ".forge")
		if err := os.MkdirAll(forgeDir, 0700); err != nil {
			return fmt.Errorf("creating .forge directory: %w", err)
		}

		name := keyName
		if name == "" {
			name = "signing-key"
		}

		privPath := filepath.Join(forgeDir, name+".pem")
		pubPath := filepath.Join(forgeDir, name+".pub")

		// Check if files exist
		if _, err := os.Stat(privPath); err == nil {
			return fmt.Errorf("key already exists at %s; remove it first or use --name", privPath)
		}

		pub, priv, err := trust.GenerateKeyPair()
		if err != nil {
			return fmt.Errorf("generating key pair: %w", err)
		}

		// Write private key (base64-encoded raw seed)
		privB64 := base64.StdEncoding.EncodeToString(priv)
		if err := os.WriteFile(privPath, []byte(privB64+"\n"), 0600); err != nil {
			return fmt.Errorf("writing private key: %w", err)
		}

		// Write public key (base64-encoded)
		pubB64 := base64.StdEncoding.EncodeToString(pub)
		if err := os.WriteFile(pubPath, []byte(pubB64+"\n"), 0644); err != nil {
			return fmt.Errorf("writing public key: %w", err)
		}

		fmt.Printf("Generated Ed25519 keypair:\n")
		fmt.Printf("  Private: %s\n", privPath)
		fmt.Printf("  Public:  %s\n", pubPath)
		return nil
	},
}

var keyTrustCmd = &cobra.Command{
	Use:   "trust <pubkey-file>",
	Short: "Add a public key to the trusted keyring",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pubFile := args[0]

		data, err := os.ReadFile(pubFile)
		if err != nil {
			return fmt.Errorf("reading public key file: %w", err)
		}

		// Validate key size
		pubBytes, err := base64.StdEncoding.DecodeString(string(data[:len(data)-1])) // strip trailing newline
		if err != nil {
			// Try without stripping
			pubBytes, err = base64.StdEncoding.DecodeString(string(data))
			if err != nil {
				return fmt.Errorf("decoding public key: %w", err)
			}
		}
		if len(pubBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid public key size: %d bytes (expected %d)", len(pubBytes), ed25519.PublicKeySize)
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("getting home directory: %w", err)
		}

		trustDir := filepath.Join(home, ".forge", "trusted-keys")
		if err := os.MkdirAll(trustDir, 0700); err != nil {
			return fmt.Errorf("creating trusted-keys directory: %w", err)
		}

		destName := filepath.Base(pubFile)
		if filepath.Ext(destName) != ".pub" {
			destName += ".pub"
		}
		destPath := filepath.Join(trustDir, destName)

		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return fmt.Errorf("writing trusted key: %w", err)
		}

		fmt.Printf("Trusted key added: %s\n", destPath)
		return nil
	},
}

var keyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List signing and trusted keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("getting home directory: %w", err)
		}

		// Check for local signing key
		forgeDir := filepath.Join(home, ".forge")
		signingKeyPath := filepath.Join(forgeDir, "signing-key.pem")
		if _, err := os.Stat(signingKeyPath); err == nil {
			fmt.Printf("Signing key: %s\n", signingKeyPath)
		} else {
			fmt.Println("Signing key: (none)")
		}

		// List trusted keys
		kr := trust.DefaultKeyring()
		ids := kr.List()

		if len(ids) == 0 {
			fmt.Println("Trusted keys: (none)")
		} else {
			fmt.Printf("Trusted keys (%d):\n", len(ids))
			for _, id := range ids {
				fmt.Printf("  - %s\n", id)
			}
		}

		return nil
	},
}

func init() {
	keyGenerateCmd.Flags().StringVar(&keyName, "name", "", "key name (default: signing-key)")

	keyCmd.AddCommand(keyGenerateCmd)
	keyCmd.AddCommand(keyTrustCmd)
	keyCmd.AddCommand(keyListCmd)
}
