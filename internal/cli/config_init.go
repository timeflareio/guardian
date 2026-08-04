package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/custody"
)

// `config init` is the first-run wizard: it writes the configuration file and,
// on request, generates the share-encryption keypair behind it. It is the one
// command that must work with no configuration present, so it resolves its own
// target path rather than requiring a loaded one.

// NewConfigInitCmd creates the config init command
func NewConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration file with defaults",
		Long: `Initialize configuration file with default values.

Creates ~/.timeflare/guardian/config.yaml with sensible defaults if it doesn't exist.
If the file already exists, this command will not overwrite it.

Critical parameters can be set via flags or will be prompted interactively.`,
		Example: `  # Initialize with interactive prompts
  guardianctl config init

  # Initialize with flags to skip all prompts  
  guardianctl config init \
    --key-name [key-name] \
    --keyring-passphrase [password] \
    --encryption-public-key [64-char-hex]

  # Initialize with auto-generated keys (encrypted at rest by default)
  guardianctl config init \
    --key-name [key-name] \
    --keyring-passphrase [password] \
    --encryption-key-passphrase [password] \
    --auto-generate-key

  # Initialize with custom keyring directory (for isolated setups)
  guardianctl config init \
    --key-name [key-name] \
    --keyring-dir [/path/to/keyring] \
    --keyring-passphrase [password] \
    --encryption-key-passphrase [password] \
    --auto-generate-key`,
		RunE: runConfigInit,
	}

	// Add flags for critical parameters
	cmd.Flags().String("encryption-public-key", "", "encryption public key (64 hex chars) - skips interactive prompt")
	cmd.Flags().Bool("auto-generate-key", false, "automatically generate encryption keys instead of prompting - skips interactive prompt")
	cmd.Flags().String("key-name", "", "timeflared keyring key name (used as guardian identifier) - skips interactive prompt")
	cmd.Flags().String("keyring-backend", "file", "keyring backend type (file, os, test, memory)")
	cmd.Flags().String("keyring-dir", "", "directory for the timeflared keyring")
	cmd.Flags().String("keyring-passphrase", "", "keyring passphrase for automated access (stored verbatim in a 0600 file)")
	cmd.Flags().String("encryption-key-passphrase", "", "passphrase encrypting the generated share key at rest (required with --auto-generate-key; stored verbatim in a 0600 file beside the key, never in the config values)")

	return cmd
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	// Resolve the target path once, up front. This used to replace a
	// package-level manager part-way through, leaving the process holding two
	// views of the configuration that could disagree.
	manager := newManager(cmd)
	configPath := manager.GetConfigPath()

	// Check if config file already exists
	if _, err := os.Stat(configPath); err == nil {
		u.TextLn("Configuration file already exists at: " + configPath)
		u.TextLn("Use 'guardianctl config list' to view current settings.")
		return nil
	}

	// No script creation needed for atomic flag-based setup

	// Get flag values
	flagEncryptionKey, _ := cmd.Flags().GetString("encryption-public-key")
	flagAutoGenerateKey, _ := cmd.Flags().GetBool("auto-generate-key")
	flagKeyName, _ := cmd.Flags().GetString("key-name")
	flagKeyringBackend, _ := cmd.Flags().GetString("keyring-backend")
	flagKeyringDir, _ := cmd.Flags().GetString("keyring-dir")
	flagKeyringPassword, _ := cmd.Flags().GetString("keyring-passphrase")
	flagEncKeyPassphrase, _ := cmd.Flags().GetString("encryption-key-passphrase")

	var encryptionKey, guardianID, keyName, keyringBackend string

	// Determine if we're using flags (atomic setup) or interactive setup
	usingFlags := flagKeyName != "" || flagEncryptionKey != "" || flagAutoGenerateKey || flagKeyringPassword != "" || flagEncKeyPassphrase != ""

	if usingFlags {
		// Validate flag combinations
		if flagEncryptionKey != "" && flagAutoGenerateKey {
			return errors.New("cannot use both --encryption-public-key and --auto-generate-key flags together")
		}

		// When using flags, require all necessary parameters for atomic setup
		missingFlags := []string{}
		if flagKeyName == "" {
			missingFlags = append(missingFlags, "--key-name")
		}
		if flagEncryptionKey == "" && !flagAutoGenerateKey {
			missingFlags = append(missingFlags, "--encryption-public-key or --auto-generate-key")
		}
		if flagKeyringBackend == "file" && flagKeyringPassword == "" {
			missingFlags = append(missingFlags, "--keyring-passphrase")
		}
		// Generated keys are encrypted at rest by default — there is no
		// plaintext generation path (key custody plan, decision 1).
		if flagAutoGenerateKey && flagEncKeyPassphrase == "" {
			missingFlags = append(missingFlags, "--encryption-key-passphrase")
		}

		if len(missingFlags) > 0 {
			return errors.Errorf("when using flags for atomic setup, all required flags must be provided. Missing: %s", strings.Join(missingFlags, ", "))
		}
	}

	needsInteractive := !usingFlags

	// No script creation needed - we'll use CGO for key generation

	// Use keyring backend from flag or default to file
	keyringBackend = flagKeyringBackend
	if keyringBackend == "" {
		keyringBackend = "file"
	}

	if flagKeyName != "" {
		keyName = flagKeyName
		if needsInteractive {
			u.Success("Using keyring key name from flag: %s\n", keyName)
		}
	} else {
		// Show header for interactive setup
		u.EmptyLine()
		u.Header("      🚀 Guardian Configuration Setup")
		u.Separator("      ═══════════════════════════════")
		u.Note("         Press Ctrl+C to exit at any time\n")

		u.Step("🔑 Step 1: Blockchain Signing Key")
		u.TextLn(ui.Indent1 + "Guardians need a signing key for blockchain transactions (registration, reveals, etc.)")
		u.TextLn(ui.Indent1 + "This key will also serve as your guardian identifier.\n")

		u.Note(ui.Indent1+"Create the signing key with guardianctl (using the %s keyring):\n", keyringBackend)

		u.TextLn(ui.Indent2 + "# Create a new signing key (shows the 24-word backup mnemonic once)")
		u.Text(ui.Indent2)
		u.Command("guardianctl wallet create --name [key-name]\n")
		u.TextLn(ui.Indent2 + "# Or restore an existing key from its 24 words")
		u.Text(ui.Indent2)
		u.Command("guardianctl wallet import-from-mnemonic --name [key-name]\n")
		u.TextLn(ui.Indent2 + "# View your wallet address")
		u.Text(ui.Indent2)
		u.Command("guardianctl wallet show-address --name [key-name]\n")

		u.Note(ui.Indent1 + "Important notes:")
		u.TextLn(ui.Indent2 + "• Back up your 24-word mnemonic securely")
		u.TextLn(ui.Indent2 + "• You'll need VEIL for gas fees, the entry fee and any deposit")
		u.TextLn(ui.Indent2 + "• The key name must exist in the guardian's keyring")
		u.TextLn(ui.Indent2 + "• This key name will also be used as your guardian identifier\n")

		keyName = u.PromptInput("🗝️  Enter keyring key name: ")
		if keyName == "" {
			return errors.New("keyring key name is required")
		}

		// Section separator
		u.Note(ui.Indent1 + "─────────────────────────────────────────────────\n")
	}

	// Use keyring key name as guardian ID (simplifies identity management)
	guardianID = keyName

	// Step 2: Keyring Passphrase Setup and Address Resolution
	var guardianAddress string
	var passphraseForFile string

	// Get passphrase from flag or prompt
	if flagKeyringPassword != "" {
		passphraseForFile = flagKeyringPassword
		if needsInteractive {
			u.Success("Using keyring passphrase from flag")
		}
	} else if keyringBackend == "file" && needsInteractive {
		u.EmptyLine()
		u.Step("🔐 Step 2: Keyring Passphrase Setup")
		u.TextLn(ui.Indent1 + "Guardian operations require automatic transaction signing for confirming")
		u.TextLn(ui.Indent1 + "and revealing secret shares. A passphrase file enables these automated")
		u.TextLn(ui.Indent1 + "transactions without manual intervention.\n")

		u.Text(ui.Indent1 + "🔑 Enter your keyring passphrase: ")
		passphraseBytes, err := u.ReadPassword()
		if err != nil {
			u.Warning("Could not read passphrase: %v", err)
		} else {
			passphrase := strings.TrimSpace(string(passphraseBytes))
			if passphrase != "" {
				passphraseForFile = passphrase
				u.Success("Passphrase collected")
			}
		}
	}

	// Resolve guardian address if we have passphrase
	if passphraseForFile != "" {
		if needsInteractive {
			u.Step(ui.Indent1 + "🔍 Resolving guardian address from key...")
		}

		guardianAddress = resolveAddressWithPassword(manager, keyName, keyringBackend, passphraseForFile, flagKeyringDir)

		// For flag-based setup, address resolution is required
		if !needsInteractive && guardianAddress == "" {
			return errors.Errorf("failed to resolve guardian address from key-name '%s' with provided passphrase - please verify the key exists and passphrase is correct", keyName)
		}

		if needsInteractive {
			if guardianAddress != "" {
				u.Success("Guardian address: %s", guardianAddress)
			} else {
				u.Warning("Could not resolve guardian address")
			}

			// Section separator
			u.Note(ui.Indent1 + "─────────────────────────────────────────────────")
		} else if guardianAddress != "" {
			// For flag-based setup, show the resolved address
			u.Success("Guardian address resolved: %s", guardianAddress)
		}
	}

	// Step 3: Encryption Key Setup
	if flagEncryptionKey != "" {
		// Using provided encryption key
		encryptionKey = flagEncryptionKey
		if needsInteractive {
			u.Success("Using encryption public key from flag: %s...\n", encryptionKey[:8])
		}
	} else if flagAutoGenerateKey {
		// Auto-generate keys in flag mode (encrypted at rest, passphrase from
		// the required flag)
		var err error
		encryptionKey, err = runCreateEncryptionKey(manager, flagEncKeyPassphrase)
		if err != nil {
			return errors.Wrap(err, "auto key generation failed")
		}
		if err := writeEncryptionKeyPassphraseFile(manager, flagEncKeyPassphrase); err != nil {
			return errors.Wrap(err, "failed to store share-key passphrase file")
		}
		if needsInteractive {
			u.Success("Encryption keys auto-generated: %s...\n", encryptionKey[:8])
		}
	} else if needsInteractive {
		// Interactive mode - prompt user for choice
		u.EmptyLine()
		u.Step("🔑 Step 3: Encryption Key Setup")
		u.TextLn(ui.Indent1 + "Guardians need encryption keys to securely receive and decrypt secret shares.")
		u.TextLn(ui.Indent1 + "You can either provide an existing public key or generate new keys.\n")

		u.Note(ui.Indent1 + "Options:")
		u.TextLn(ui.Indent2 + "1. Generate new keys automatically (recommended for new guardians)")
		u.TextLn(ui.Indent2 + "2. Provide existing public key (if you already have encryption keys)\n")

		choice := u.PromptInput(ui.Indent1 + "🔀 Generate new keys? [Y/n]: ")
		choice = strings.ToLower(strings.TrimSpace(choice))

		if choice == "" || choice == "y" || choice == "yes" {
			// Generate new keys — encrypted at rest by default (there is no
			// plaintext generation path)
			u.EmptyLine()
			u.Note(ui.Indent1 + "The private key is stored encrypted at rest. Choose a passphrase;")
			u.Note(ui.Indent1 + "it is kept in a 0600 file beside the key so the daemon can run unattended.")
			passphrase, err := u.NewPassphrase("share-encryption private key")
			if err != nil {
				return errors.Wrap(err, "failed to read share-key passphrase")
			}

			u.TextLn("\n" + ui.Indent1 + "⚡ Generating encryption keys...")

			encryptionKey, err = runCreateEncryptionKey(manager, passphrase)
			if err != nil {
				u.Warning("Key generation failed: %v", err)
				u.Note(ui.Indent1 + "You can set the encryption key later with:" + ui.Indent1)
				u.Command("guardianctl config set encryption-public-key <64-hex-chars>\n\n")
				encryptionKey = "" // Continue without key
			} else {
				if err := writeEncryptionKeyPassphraseFile(manager, passphrase); err != nil {
					return errors.Wrap(err, "failed to store share-key passphrase file")
				}
				u.Success("Encryption keys generated successfully!")
				privateKeyPath := manager.GetPrivateKeyPath()
				publicKeyPath := manager.GetPublicKeyPath()
				u.TextLn(ui.Indent1 + "📁 Key locations:")
				u.TextLn(ui.Indent2 + "• Private key: " + privateKeyPath + " (encrypted at rest — keep it SECRET!)")
				u.TextLn(ui.Indent2 + "• Passphrase:  " + custody.SiblingPassphrasePath(privateKeyPath))
				u.TextLn(ui.Indent2 + "• Public key:  " + publicKeyPath)
				u.TextLn(ui.Indent2 + "• Public key hex: " + encryptionKey)
				u.Warning("CRITICAL: Back up your private key securely!")
				u.TextLn(ui.Indent2 + "• Run 'guardianctl key backup' after registration for a portable encrypted bundle")
				u.TextLn(ui.Indent2 + "• Without the key, you cannot decrypt shares sent to you")
				u.TextLn(ui.Indent2 + "• Lost keys prevent participation in reveals, resulting in slashing penalties\n")
			}
		} else {
			// Manual key input
			encryptionKey = u.PromptInput("\n" + ui.Indent1 + "🔑 Enter your encryption public key (64 hex characters): ")
			if len(encryptionKey) != 64 {
				u.Warning("Invalid key length: expected 64 characters, got %d", len(encryptionKey))
				u.Note(ui.Indent1 + "You can set the correct encryption key later with:" + ui.Indent1)
				u.Command("guardianctl config set encryption-public-key <64-hex-chars>\n\n")
				encryptionKey = "" // Continue without key
			} else {
				u.Success("Using provided encryption key: %s...\n", encryptionKey[:8])
			}
		}

		// Section separator
		if encryptionKey != "" {
			u.Note(ui.Indent1 + "─────────────────────────────────────────────────\n")
		}
	} else {
		// Non-interactive mode without flags - should not happen due to validation above
		return errors.New("encryption key required: use --encryption-public-key or --auto-generate-key")
	}

	// Set the user-provided values using the config manager
	if encryptionKey != "" {
		if err := manager.SetWithoutValidation("encryption_public_key", encryptionKey); err != nil {
			return errors.Wrap(err, "failed to set encryption key")
		}
		// Set the private key path using config-derived path
		privateKeyPath := manager.GetPrivateKeyPath()
		if err := manager.SetWithoutValidation("encryption_private_key_path", privateKeyPath); err != nil {
			return errors.Wrap(err, "failed to set encryption private key path")
		}
	}
	if err := manager.SetWithoutValidation("guardian_id", guardianID); err != nil {
		return errors.Wrap(err, "failed to set guardian ID")
	}
	if err := manager.SetWithoutValidation("key_name", keyName); err != nil {
		return errors.Wrap(err, "failed to set key name")
	}
	if err := manager.SetWithoutValidation("keyring_backend", keyringBackend); err != nil {
		return errors.Wrap(err, "failed to set keyring backend")
	}

	// Set keyring directory if provided (optional flag)
	if flagKeyringDir != "" {
		if err := manager.SetWithoutValidation("keyring_dir", flagKeyringDir); err != nil {
			return errors.Wrap(err, "failed to set keyring directory")
		}
	}

	// Set the guardian address if we resolved it earlier
	if guardianAddress != "" {
		if err := manager.SetWithoutValidation("guardian_address", guardianAddress); err != nil {
			return errors.Wrap(err, "failed to set guardian address")
		}
	}

	// Write passphrase file now that all steps are complete
	if passphraseForFile != "" {
		keyDir := manager.GetKeyDirectory()
		passphraseFile := filepath.Join(keyDir, "keyring_passphrase")

		// Ensure directory exists
		if err := ensureGuardianDirectory(manager); err != nil {
			return errors.Wrap(err, "failed to create guardian directory")
		}

		if err := custody.WritePassphraseFile(passphraseFile, passphraseForFile); err != nil {
			return errors.Wrap(err, "failed to create passphrase file")
		}
		// Set the passphrase file path in config
		if err := manager.SetWithoutValidation("keyring_passphrase", passphraseFile); err != nil {
			return errors.Wrap(err, "failed to set keyring passphrase file")
		}
	}

	// Save the configuration (all at once)
	if err := manager.Save(); err != nil {
		return errors.Wrap(err, "failed to initialize config")
	}

	// Success summary with colors
	u.EmptyLine()
	u.Separator("    Configuration Initialized Successfully")
	u.EmptyLine()

	// Configuration summary
	u.Step("📋 Configuration Summary:")
	u.Text(ui.Indent1 + "📁 Config File:           ")
	u.Path("%s\n", configPath)
	u.Text(ui.Indent1 + "🗝️  Guardian Identity:      ")
	u.Value("%s\n", keyName)
	u.Text(ui.Indent1 + "🔐 Keyring Backend:        ")
	u.Value("%s\n", keyringBackend)
	u.Text(ui.Indent1 + "🔑 Encryption Public Key:  ")
	if encryptionKey != "" {
		u.Value("%s\n", encryptionKey)
		u.Text(ui.Indent1 + "🔒 Encryption Private Key: ")
		privateKeyPath := manager.GetPrivateKeyPath()
		u.Path("%s\n", privateKeyPath)
	} else {
		u.Note("(empty - set later with 'guardianctl config set encryption-public-key <key>')")
	}

	// Next steps with colors
	u.Step("🚀 Next Steps:")
	u.Text(ui.Indent1 + "• ")
	u.Command("guardianctl config list")
	u.TextLn(" - view all settings")

	u.Text(ui.Indent1 + "• ")
	u.Command("guardianctl register")
	u.TextLn(" - register your guardian with the network\n")

	return nil
}

// resolveAddressWithPassword resolves the guardian address from the keyring
// in-process using the provided passphrase (the file may not exist yet during
// init). Returns "" on failure.
func resolveAddressWithPassword(manager *config.Manager, keyName, keyringBackend, passphrase string, keyringDir ...string) string {
	keyDir := manager.GetKeyDirectory()
	if len(keyringDir) > 0 && keyringDir[0] != "" {
		keyDir = keyringDir[0]
	}

	address, err := chain.ResolveKeyAddressWithPassphrase(keyringBackend, keyDir, keyName, passphrase)
	if err != nil {
		return ""
	}
	if err := config.ValidateGuardianAddress(address); err != nil {
		return ""
	}
	return address
}
