package cli

import (
	"bufio"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/custody"
	"golang.org/x/term"
)

// readPasswordInput reads password input with hidden characters
func readPasswordInput() ([]byte, error) {
	// Get file descriptor for stdin
	fd := int(os.Stdin.Fd())

	// Check if stdin is a terminal
	if !term.IsTerminal(fd) {
		// Fallback for non-terminal input (e.g., pipes, redirects)
		reader := bufio.NewReader(os.Stdin)
		password, err := reader.ReadBytes('\n')
		return password, err
	}

	// Set up signal handling to restore terminal on Ctrl+C
	c := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// Get current terminal state to restore if interrupted
	oldState, err := term.GetState(fd)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get terminal state")
	}

	// Goroutine to handle signals and restore terminal
	go func() {
		select {
		case <-c:
			_ = term.Restore(fd, oldState) // Error ignored during signal handling
			printTextLn("\n\n⚠️  Password setup cancelled. Configuration incomplete.")
			os.Exit(1)
		case <-done:
			return
		}
	}()

	// Read password with hidden input
	password, err := term.ReadPassword(fd)

	// Stop signal handling
	signal.Stop(c)
	done <- true

	if err != nil {
		return nil, err
	}

	// Print newline since ReadPassword doesn't
	printEmptyLine()

	return password, nil
}

// ensureGuardianDirectory creates the guardian directory if it doesn't exist
func ensureGuardianDirectory(manager *config.Manager) error {
	// Use the config manager to get the correct key directory
	return manager.EnsureDirectoriesExist()
}

// stripAnsiCodes removes ANSI color codes for accurate length calculation
func stripAnsiCodes(s string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiRegex.ReplaceAllString(s, "")
}

// showNoConfigMessage displays a consistent "no configuration found" message
func showNoConfigMessage(configPath string) {
	printEmptyLine()
	printSeparator("No Configuration Found!")
	printEmptyLine()

	printNote("Guardian operations require a configuration file")
	printText(indent1 + "Expected config file: ")
	printPath("%s\n", configPath)

	printText(indent1 + "Run: ")
	printCommand("guardiand config init")
	printTextLn(" to create the configuration file.")

	printText(indent1 + "Or use: ")
	printCommand("--config-path /path/to/config.yaml")
	printTextLn(" to specify a custom location.\n")

	printNote("The configuration setup will guide you through:")
	printTextLn(indent1 + "• Setting up your guardian signing key")
	printTextLn(indent1 + "• Generating encryption keys for secret shares")
	printTextLn(indent1 + "• Configuring blockchain connection settings\n")
}

// promptForInput prompts the user for input with a message and returns the trimmed response
func promptForInput(prompt string) string {
	printText(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

// NewConfigCmd creates the config command
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage guardian configuration",
		Long: `Manage guardian configuration settings.

Configuration is stored in ~/.timeflare/guardian/config.yaml by default.
Use --config-path to specify a different location.`,
		RunE: runConfigHelp,
	}

	// Add subcommands
	cmd.AddCommand(NewConfigInitCmd())
	cmd.AddCommand(NewConfigSetCmd())
	cmd.AddCommand(NewConfigGetCmd())
	cmd.AddCommand(NewConfigListCmd())
	cmd.AddCommand(NewConfigValidateCmd())
	cmd.AddCommand(NewConfigDoctorCmd())
	cmd.AddCommand(NewConfigCreateEncryptionKeyCmd())
	cmd.AddCommand(NewConfigMigrateKeyCmd())
	cmd.AddCommand(NewConfigSetDashboardPasswordCmd())

	return cmd
}

// NewConfigDoctorCmd creates the config doctor command
func NewConfigDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report effective configuration and check operational readiness",
		Long: `Report the configuration exactly as the running service would consume it
(file + GUARDIAN_* environment overrides, typed effective values), then check:
- cross-field validity (ports, endpoints, durations)
- the signing key resolves in the keyring
- the share-decryption private key loads`,
		RunE: runConfigDoctor,
	}
}

func runConfigDoctor(cmd *cobra.Command, args []string) error {
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}
	effective := manager.GetConfig()

	printEmptyLine()
	printSeparator("🩺 Guardian Configuration Doctor")
	printEmptyLine()
	printNote("Effective values (file + environment overrides), as the service consumes them:")
	printEmptyLine()

	for _, group := range config.GroupOrder() {
		items := manager.ListAllGrouped()[group]
		keys := make([]string, 0, len(items))
		for key := range items {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		printHeader("%s:", group)
		for _, key := range keys {
			item := items[key]
			marker := " "
			if !item.IsDefault {
				marker = "*"
			}
			printf(indent1+"%s %-28s %s\n", marker, key, item.Value)
		}
		printEmptyLine()
	}
	printNote("(* = differs from default)")
	printEmptyLine()

	failed := false

	if err := effective.Validate(); err != nil {
		printError("Validation: %v", err)
		failed = true
	} else {
		printSuccess("Validation: configuration is consistent")
	}

	if address, err := chain.ResolveKeyAddress(effective, effective.KeyName); err != nil {
		printError("Signing key: %v", err)
		failed = true
	} else {
		printSuccess("Signing key: %s resolves to %s", effective.KeyName, address)
		if effective.GuardianAddress != "" && effective.GuardianAddress != address {
			printError("guardian_address (%s) does not match the key's address (%s)", effective.GuardianAddress, address)
			failed = true
		}
	}

	if _, err := effective.GetEncryptionPrivateKey(); err != nil {
		printError("Encryption key: %v", err)
		failed = true
	} else {
		printSuccess("Encryption key: loads from %s", effective.EncryptionPrivateKeyPath)
	}

	reportDashboardExposure(effective)

	printEmptyLine()
	if failed {
		return errors.New("configuration doctor found problems")
	}
	printSuccess("Guardian configuration is operational ✓")
	printEmptyLine()
	return nil
}

// runConfigHelp shows help and checks if config exists. It resolves its own
// manager rather than requiring one: the whole point of this command is to be
// useful on a host that has no configuration yet.
func runConfigHelp(cmd *cobra.Command, args []string) error {
	manager := newManager(cmd)
	configPath := manager.GetConfigPath()

	if !manager.Exists() {
		showNoConfigMessage(configPath)
		return nil
	}

	printEmptyLine()
	printNote("Configuration found:")
	printText(indent1 + "✓ Config: ")
	printCommand("%s\n", configPath)
	printEmptyLine()
	printText(indent1 + "Use: ")
	printCommand("guardiand config list")
	printEmptyLine()
	printEmptyLine()

	// Show normal help
	return cmd.Help()
}

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
  guardiand config init

  # Initialize with flags to skip all prompts  
  guardiand config init \
    --key-name [key-name] \
    --keyring-passphrase [password] \
    --encryption-public-key [64-char-hex]

  # Initialize with auto-generated keys (encrypted at rest by default)
  guardiand config init \
    --key-name [key-name] \
    --keyring-passphrase [password] \
    --encryption-key-passphrase [password] \
    --auto-generate-key

  # Initialize with custom keyring directory (for isolated setups)
  guardiand config init \
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

// NewConfigSetCmd creates the config set command
func NewConfigSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set a configuration value for the specified key.

Use 'guardiand config list' to see all available configuration parameters.
Parameters can be specified using either underscore (chain_id) or hyphen (chain-id) format.`,
		Example: `  # Set chain ID
  guardiand config set chain-id timeflare-1

  # Set guardian key name
  guardiand config set key-name guardian

  # Set RPC endpoint
  guardiand config set rpc-endpoint http://localhost:26657

  # Set stake amount
  guardiand config set stake-amount 15000uveil

  # Set numeric values
  guardiand config set retry-attempts 5

  # Set boolean values
  guardiand config set enable-metrics false`,
		Args: cobra.ExactArgs(2),
		RunE: runConfigSet,
	}

	return cmd
}

// NewConfigGetCmd creates the config get command
func NewConfigGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Long:  `Get the current value for the specified configuration key.`,
		Example: `  # Get chain ID
  guardiand config get chain-id

  # Get key name
  guardiand config get key-name`,
		Args: cobra.ExactArgs(1),
		RunE: runConfigGet,
	}

	return cmd
}

// NewConfigListCmd creates the config list command
func NewConfigListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all configuration values",
		Long:  `Display all current configuration key-value pairs.`,
		Example: `  # List all configuration
  guardiand config list`,
		RunE: runConfigList,
	}

	return cmd
}

// NewConfigValidateCmd creates the config validate command
func NewConfigValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration",
		Long: `Validate the current configuration for completeness and correctness.

This checks:
- All required fields are present
- Values are in correct format
- Guardian key exists in timeflared keyring (if possible)`,
		Example: `  # Validate current configuration
  guardiand config validate`,
		RunE: runConfigValidate,
	}

	return cmd
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	// Resolve the target path once, up front. This used to replace a
	// package-level manager part-way through, leaving the process holding two
	// views of the configuration that could disagree.
	manager := newManager(cmd)
	configPath := manager.GetConfigPath()

	// Check if config file already exists
	if _, err := os.Stat(configPath); err == nil {
		printTextLn("Configuration file already exists at: " + configPath)
		printTextLn("Use 'guardiand config list' to view current settings.")
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
			printSuccess("Using keyring key name from flag: %s\n", keyName)
		}
	} else {
		// Show header for interactive setup
		printEmptyLine()
		printHeader("      🚀 Guardian Configuration Setup")
		printSeparator("      ═══════════════════════════════")
		printNote("         Press Ctrl+C to exit at any time\n")

		printStep("🔑 Step 1: Blockchain Signing Key")
		printTextLn(indent1 + "Guardians need a signing key for blockchain transactions (registration, reveals, etc.)")
		printTextLn(indent1 + "This key will also serve as your guardian identifier.\n")

		printNote(indent1+"Create the signing key with guardiand (using the %s keyring):\n", keyringBackend)

		printTextLn(indent2 + "# Create a new signing key (shows the 24-word backup mnemonic once)")
		printText(indent2)
		printCommand("guardiand wallet create --name [key-name]\n")
		printTextLn(indent2 + "# Or restore an existing key from its 24 words")
		printText(indent2)
		printCommand("guardiand wallet import-from-mnemonic --name [key-name]\n")
		printTextLn(indent2 + "# View your wallet address")
		printText(indent2)
		printCommand("guardiand wallet show-address --name [key-name]\n")

		printNote(indent1 + "Important notes:")
		printTextLn(indent2 + "• Back up your 24-word mnemonic securely")
		printTextLn(indent2 + "• You'll need VEIL for gas fees, the entry fee and any deposit")
		printTextLn(indent2 + "• The key name must exist in the guardian's keyring")
		printTextLn(indent2 + "• This key name will also be used as your guardian identifier\n")

		keyName = promptForInput("🗝️  Enter keyring key name: ")
		if keyName == "" {
			return errors.New("keyring key name is required")
		}

		// Section separator
		printNote(indent1 + "─────────────────────────────────────────────────\n")
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
			printSuccess("Using keyring passphrase from flag")
		}
	} else if keyringBackend == "file" && needsInteractive {
		printEmptyLine()
		printStep("🔐 Step 2: Keyring Passphrase Setup")
		printTextLn(indent1 + "Guardian operations require automatic transaction signing for confirming")
		printTextLn(indent1 + "and revealing secret shares. A passphrase file enables these automated")
		printTextLn(indent1 + "transactions without manual intervention.\n")

		printText(indent1 + "🔑 Enter your keyring passphrase: ")
		passphraseBytes, err := readPasswordInput()
		if err != nil {
			printWarning("Could not read passphrase: %v", err)
		} else {
			passphrase := strings.TrimSpace(string(passphraseBytes))
			if passphrase != "" {
				passphraseForFile = passphrase
				printSuccess("Passphrase collected")
			}
		}
	}

	// Resolve guardian address if we have passphrase
	if passphraseForFile != "" {
		if needsInteractive {
			printStep(indent1 + "🔍 Resolving guardian address from key...")
		}

		guardianAddress = resolveAddressWithPassword(manager, keyName, keyringBackend, passphraseForFile, flagKeyringDir)

		// For flag-based setup, address resolution is required
		if !needsInteractive && guardianAddress == "" {
			return errors.Errorf("failed to resolve guardian address from key-name '%s' with provided passphrase - please verify the key exists and passphrase is correct", keyName)
		}

		if needsInteractive {
			if guardianAddress != "" {
				printSuccess("Guardian address: %s", guardianAddress)
			} else {
				printWarning("Could not resolve guardian address")
			}

			// Section separator
			printNote(indent1 + "─────────────────────────────────────────────────")
		} else if guardianAddress != "" {
			// For flag-based setup, show the resolved address
			printSuccess("Guardian address resolved: %s", guardianAddress)
		}
	}

	// Step 3: Encryption Key Setup
	if flagEncryptionKey != "" {
		// Using provided encryption key
		encryptionKey = flagEncryptionKey
		if needsInteractive {
			printSuccess("Using encryption public key from flag: %s...\n", encryptionKey[:8])
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
			printSuccess("Encryption keys auto-generated: %s...\n", encryptionKey[:8])
		}
	} else if needsInteractive {
		// Interactive mode - prompt user for choice
		printEmptyLine()
		printStep("🔑 Step 3: Encryption Key Setup")
		printTextLn(indent1 + "Guardians need encryption keys to securely receive and decrypt secret shares.")
		printTextLn(indent1 + "You can either provide an existing public key or generate new keys.\n")

		printNote(indent1 + "Options:")
		printTextLn(indent2 + "1. Generate new keys automatically (recommended for new guardians)")
		printTextLn(indent2 + "2. Provide existing public key (if you already have encryption keys)\n")

		choice := promptForInput(indent1 + "🔀 Generate new keys? [Y/n]: ")
		choice = strings.ToLower(strings.TrimSpace(choice))

		if choice == "" || choice == "y" || choice == "yes" {
			// Generate new keys — encrypted at rest by default (there is no
			// plaintext generation path)
			printEmptyLine()
			printNote(indent1 + "The private key is stored encrypted at rest. Choose a passphrase;")
			printNote(indent1 + "it is kept in a 0600 file beside the key so the daemon can run unattended.")
			passphrase, err := promptNewPassphrase("share-encryption private key")
			if err != nil {
				return errors.Wrap(err, "failed to read share-key passphrase")
			}

			printTextLn("\n" + indent1 + "⚡ Generating encryption keys...")

			encryptionKey, err = runCreateEncryptionKey(manager, passphrase)
			if err != nil {
				printWarning("Key generation failed: %v", err)
				printNote(indent1 + "You can set the encryption key later with:" + indent1)
				printCommand("guardiand config set encryption-public-key <64-hex-chars>\n\n")
				encryptionKey = "" // Continue without key
			} else {
				if err := writeEncryptionKeyPassphraseFile(manager, passphrase); err != nil {
					return errors.Wrap(err, "failed to store share-key passphrase file")
				}
				printSuccess("Encryption keys generated successfully!")
				privateKeyPath := manager.GetPrivateKeyPath()
				publicKeyPath := manager.GetPublicKeyPath()
				printTextLn(indent1 + "📁 Key locations:")
				printTextLn(indent2 + "• Private key: " + privateKeyPath + " (encrypted at rest — keep it SECRET!)")
				printTextLn(indent2 + "• Passphrase:  " + custody.SiblingPassphrasePath(privateKeyPath))
				printTextLn(indent2 + "• Public key:  " + publicKeyPath)
				printTextLn(indent2 + "• Public key hex: " + encryptionKey)
				printWarning("CRITICAL: Back up your private key securely!")
				printTextLn(indent2 + "• Run 'guardiand key backup' after registration for a portable encrypted bundle")
				printTextLn(indent2 + "• Without the key, you cannot decrypt shares sent to you")
				printTextLn(indent2 + "• Lost keys prevent participation in reveals, resulting in slashing penalties\n")
			}
		} else {
			// Manual key input
			encryptionKey = promptForInput("\n" + indent1 + "🔑 Enter your encryption public key (64 hex characters): ")
			if len(encryptionKey) != 64 {
				printWarning("Invalid key length: expected 64 characters, got %d", len(encryptionKey))
				printNote(indent1 + "You can set the correct encryption key later with:" + indent1)
				printCommand("guardiand config set encryption-public-key <64-hex-chars>\n\n")
				encryptionKey = "" // Continue without key
			} else {
				printSuccess("Using provided encryption key: %s...\n", encryptionKey[:8])
			}
		}

		// Section separator
		if encryptionKey != "" {
			printNote(indent1 + "─────────────────────────────────────────────────\n")
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
	printEmptyLine()
	printSeparator("    Configuration Initialized Successfully")
	printEmptyLine()

	// Configuration summary
	printStep("📋 Configuration Summary:")
	printText(indent1 + "📁 Config File:           ")
	printPath("%s\n", configPath)
	printText(indent1 + "🗝️  Guardian Identity:      ")
	printValue("%s\n", keyName)
	printText(indent1 + "🔐 Keyring Backend:        ")
	printValue("%s\n", keyringBackend)
	printText(indent1 + "🔑 Encryption Public Key:  ")
	if encryptionKey != "" {
		printValue("%s\n", encryptionKey)
		printText(indent1 + "🔒 Encryption Private Key: ")
		privateKeyPath := manager.GetPrivateKeyPath()
		printPath("%s\n", privateKeyPath)
	} else {
		printNote("(empty - set later with 'guardiand config set encryption-public-key <key>')")
	}

	// Next steps with colors
	printStep("🚀 Next Steps:")
	printText(indent1 + "• ")
	printCommand("guardiand config list")
	printTextLn(" - view all settings")

	printText(indent1 + "• ")
	printCommand("guardiand register")
	printTextLn(" - register your guardian with the network\n")

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

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]

	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist for modifications
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Set the value
	if err := manager.Set(key, value); err != nil {
		return errors.Wrap(err, "failed to set config")
	}

	// Save the config
	if err := manager.Save(); err != nil {
		return errors.Wrap(err, "failed to save config")
	}

	printf("Set %s = %s\n\n", key, value)
	return nil
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	key := args[0]

	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Get the value
	value, err := manager.Get(key)
	if err != nil {
		return err
	}

	printTextLn(value + "\n")
	return nil
}

func runConfigList(cmd *cobra.Command, args []string) error {
	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Get grouped configuration
	groups := manager.ListAllGrouped()

	// Print header
	printHeader("Guardian Configuration")
	printText("Config file: ")
	printPath("%s\n\n", manager.GetConfigPath())

	// Define group order for consistent display
	groupOrder := []string{
		"Network Configuration",
		"Guardian Identity & Keys",
		"Staking & Economics",
		"Networking & Timeouts",
		"Guardian Service",
		"Registration Defaults",
		"Blockchain Integration",
		"Monitoring & Observability",
		"File Paths & Storage",
		"Performance",
		"Operational",
	}

	for i, groupName := range groupOrder {
		group, exists := groups[groupName]
		if !exists || len(group) == 0 {
			continue
		}

		// Print group header
		printHeader("▶ %s", groupName)

		// Sort keys within group for consistent display
		var keys []string
		for key := range group {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		// Print each configuration item
		for _, key := range keys {
			item := group[key]
			value := item.Value

			// Format empty values nicely - we'll handle this inline

			// Truncate very long values for display
			displayValue := value
			if len(value) > 60 {
				displayValue = value[:57] + "..."
			}

			// Calculate actual display length (without color codes)
			actualLen := 2 + 25 + 3 + len(stripAnsiCodes(displayValue)) // "  " + key + " = " + value

			// Print key-value pair with indentation
			printText("  ")
			printKey("%-25s", key)
			printText(" = ")
			if value == "" {
				printText("(empty)")
			} else {
				printValue("%s", displayValue)
			}

			// Add description aligned at column 90
			targetCol := 90
			if actualLen < targetCol {
				spaces := targetCol - actualLen
				printf("%*s", spaces, "")
			} else {
				printText("  ")
			}

			descText := strings.ToLower(item.Description)
			if len(descText) > 70 {
				descText = descText[:67] + "..."
			}
			printf("# %s", descText)
			printEmptyLine()
		}

		// Add spacing between groups (except for last group)
		if i < len(groupOrder)-1 {
			printEmptyLine()
		}
	}

	printf("\nUse 'guardiand config set <key> <value>' to change values.\n")
	printf("Use 'guardiand config get <key>' to view a specific value.\n\n")

	return nil
}

func runConfigValidate(cmd *cobra.Command, args []string) error {
	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Validate config
	if err := manager.Validate(); err != nil {
		return errors.Wrap(err, "configuration validation failed")
	}

	printTextLn("Configuration is valid ✓\n")
	return nil
}

// NewConfigCreateEncryptionKeyCmd creates the config create-encryption-key command
func NewConfigCreateEncryptionKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-encryption-key",
		Short: "Create encryption keys for guardian operations",
		Long: `Create encryption keys for guardian operations.

This command generates a new X25519 encryption keypair and saves it to the specified directory.
The private key is saved as a passphrase-encrypted envelope (encrypted-by-default —
there is no plaintext generation path), and the public key is saved as a hex string.

Key files created:
  {file-name}.key - Private key (encrypted envelope, 600 permissions)
  {file-name}.hex - Public key (64 hex characters, 644 permissions)

The passphrase comes from --passphrase-file, or an interactive prompt.

This command will NEVER overwrite existing keys and will error immediately if keys already exist.`,
		Example: `  # Create keys with default settings (prompts for a passphrase)
  guardiand config create-encryption-key

  # Create keys non-interactively
  guardiand config create-encryption-key --passphrase-file /secure/kek

  # Create keys with custom filename
  guardiand config create-encryption-key --file-name my-guardian

  # Create keys in custom directory
  guardiand config create-encryption-key --directory /path/to/keys`,
		RunE: runConfigCreateEncryptionKey,
	}

	// Add flags
	cmd.Flags().String("file-name", "encryption", "Base filename for key files (creates {file-name}.key and {file-name}.hex)")
	cmd.Flags().String("directory", "", "Directory to create keys in (default: derived from config path)")
	cmd.Flags().String("passphrase-file", "", "File containing the passphrase for the encrypted private key (default: interactive prompt)")

	return cmd
}

// NewConfigMigrateKeyCmd creates the config migrate-key command.
func NewConfigMigrateKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate-key",
		Short: "Encrypt an existing plaintext share key in place",
		Long: `Encrypt an existing plaintext share-encryption private key in place.

Keys generated before the encrypted-at-rest format (raw 32-byte files) keep
loading, but this command upgrades them to the versioned encrypted envelope.
The passphrase comes from --passphrase-file, an already-configured passphrase
file, or an interactive prompt (in which case it is stored in a 0600 file
beside the key so the daemon can keep running unattended — pass
--no-passphrase-file to manage the file yourself).

The key file is replaced atomically and the result is verified by decrypting
it again before the command reports success.`,
		Example: `  # Interactive migration (prompts for a passphrase, asks about backups)
  guardiand config migrate-key

  # Non-interactive migration for automated fleets
  guardiand config migrate-key --passphrase-file /secure/kek --accept`,
		RunE: runConfigMigrateKey,
	}

	cmd.Flags().String("passphrase-file", "", "File containing the encryption passphrase (default: configured file, then interactive prompt)")
	cmd.Flags().Bool("accept", false, "skip the backup confirmation prompt")
	cmd.Flags().Bool("no-passphrase-file", false, "do not write a passphrase file for a prompted passphrase (the daemon will need one supplied another way)")

	return cmd
}

func runConfigMigrateKey(cmd *cobra.Command, args []string) error {
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}
	effective := manager.GetConfig()
	keyPath := effective.EncryptionPrivateKeyPath

	flagPassphraseFile, _ := cmd.Flags().GetString("passphrase-file")
	accept, _ := cmd.Flags().GetBool("accept")
	noPassphraseFile, _ := cmd.Flags().GetBool("no-passphrase-file")

	encrypted, err := custody.IsEncryptedKeyFile(keyPath)
	if err != nil {
		return errors.Wrapf(err, "failed to read private key at %s", keyPath)
	}
	if encrypted {
		printSuccess("Private key at %s is already encrypted — nothing to do\n", keyPath)
		return nil
	}

	// Load the plaintext key (no passphrase needed for the legacy format)
	key, err := custody.LoadShareKey(keyPath, func() (string, error) {
		return "", errors.New("unexpected passphrase request for plaintext key")
	})
	if err != nil {
		return errors.Wrap(err, "failed to load plaintext private key")
	}
	defer custody.Zero(key[:])

	// Resolve the passphrase: explicit flag > already-configured file >
	// interactive prompt.
	var passphrase string
	passphraseFromFile := true
	switch {
	case flagPassphraseFile != "":
		passphrase, err = custody.ReadPassphraseFile(flagPassphraseFile)
		if err != nil {
			return err
		}
	default:
		if path, resolveErr := custody.ResolvePassphrasePath(effective.EncryptionKeyPassphrase, keyPath); resolveErr == nil {
			passphrase, err = custody.ReadPassphraseFile(path)
			if err != nil {
				return err
			}
			printNote("Using existing passphrase file: %s", path)
		} else {
			passphraseFromFile = false
			passphrase, err = promptNewPassphrase("share-encryption private key")
			if err != nil {
				return err
			}
		}
	}
	if passphrase == "" {
		return errors.New("encryption passphrase cannot be empty")
	}

	// Backup confirmation — encrypting in place with a lost passphrase is
	// exactly the total-loss scenario this feature exists to prevent.
	if !accept {
		printWarning("This encrypts %s in place. If the passphrase is lost, the key is unrecoverable.", keyPath)
		if !promptForConfirmation("Confirm you hold an independent backup (guardiand key backup, or the 24-word mnemonic)") {
			printWarning("Migration cancelled — run 'guardiand key backup' first.")
			printEmptyLine()
			return nil
		}
	}

	if err := custody.SaveEncryptedShareKey(keyPath, key, passphrase); err != nil {
		return err
	}

	// Store the passphrase beside the key for unattended operation, unless
	// it already came from a file or the operator opted out.
	if !passphraseFromFile && flagPassphraseFile == "" && !noPassphraseFile {
		if err := writeEncryptionKeyPassphraseFile(manager, passphrase); err != nil {
			return errors.Wrap(err, "failed to store share-key passphrase file")
		}
		if err := manager.Save(); err != nil {
			return errors.Wrap(err, "failed to save config")
		}
		printNote("Passphrase stored at %s (0600) so the daemon can decrypt unattended", custody.SiblingPassphrasePath(keyPath))
	}
	if flagPassphraseFile != "" {
		if err := manager.SetWithoutValidation("encryption_key_passphrase", flagPassphraseFile); err != nil {
			return errors.Wrap(err, "failed to set encryption_key_passphrase")
		}
		if err := manager.Save(); err != nil {
			return errors.Wrap(err, "failed to save config")
		}
	}

	// Verify: the envelope must decrypt back to the exact same key.
	reloaded, err := custody.LoadShareKey(keyPath, func() (string, error) { return passphrase, nil })
	if err != nil {
		return errors.Wrap(err, "post-migration verification failed")
	}
	defer custody.Zero(reloaded[:])
	if reloaded != key {
		return errors.New("post-migration verification failed: decrypted key does not match the original")
	}

	printSuccess("Private key at %s is now encrypted at rest ✓\n", keyPath)
	return nil
}

func runConfigCreateEncryptionKey(cmd *cobra.Command, args []string) error {
	manager, _, err := optionalConfig(cmd)
	if err != nil {
		return err
	}

	// Get flag values
	fileName, _ := cmd.Flags().GetString("file-name")
	directory, _ := cmd.Flags().GetString("directory")

	// Determine directory - use flag if provided, otherwise derive from config path
	var expandedDir string
	if directory != "" {
		// User provided explicit directory, expand it
		expandedDir = os.ExpandEnv(directory)
		if strings.HasPrefix(directory, "~") {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return errors.Wrap(err, "failed to get home directory: %w")
			}
			if directory == "~" {
				expandedDir = homeDir
			} else if strings.HasPrefix(directory, "~/") {
				expandedDir = filepath.Join(homeDir, directory[2:])
			}
		}
	} else {
		// No directory flag provided, derive from config path
		expandedDir = manager.GetKeyDirectory()
	}

	// Define file paths
	privateKeyPath := filepath.Join(expandedDir, fileName+".key")
	publicKeyPath := filepath.Join(expandedDir, fileName+".hex")

	// Show header
	printEmptyLine()
	printSeparator("🔑 Creating Encryption Keys")
	printEmptyLine()

	// Check if keys already exist
	if _, err := os.Stat(privateKeyPath); err == nil {
		printError("Private key already exists: %s", privateKeyPath)
		printNote("To generate new keys, first move the existing keys:")
		printf(indent1+"mv %s %s.backup\n", privateKeyPath, privateKeyPath)
		printf(indent1+"mv %s %s.backup\n\n", publicKeyPath, publicKeyPath)
		return errors.New("encryption keys already exist - will not overwrite")
	}

	if _, err := os.Stat(publicKeyPath); err == nil {
		printError("Public key already exists: %s", publicKeyPath)
		printNote("To generate new keys, first move the existing keys:")
		printf(indent1+"mv %s %s.backup\n", privateKeyPath, privateKeyPath)
		printf(indent1+"mv %s %s.backup\n\n", publicKeyPath, publicKeyPath)
		return errors.New("encryption keys already exist - will not overwrite")
	}

	// Resolve the encryption passphrase (encrypted-by-default — no plaintext
	// generation path)
	var passphrase string
	if passphraseFile, _ := cmd.Flags().GetString("passphrase-file"); passphraseFile != "" {
		var err error
		passphrase, err = custody.ReadPassphraseFile(passphraseFile)
		if err != nil {
			return err
		}
	} else {
		var err error
		passphrase, err = promptNewPassphrase("private key")
		if err != nil {
			return errors.Wrap(err, "failed to read passphrase")
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(expandedDir, 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory '%s'", expandedDir)
	}

	printf("📁 Directory: %s\n", expandedDir)
	printf("🔑 Key name:  %s\n\n", fileName)
	printTextLn("⚡ Generating encryption keys...")

	publicKeyHex, err := generateEncryptedKeypair(privateKeyPath, publicKeyPath, passphrase)
	if err != nil {
		printError("Key generation failed: %v", err)
		return err
	}

	// Success summary
	printEmptyLine()
	printSeparator("Encryption Keys Created Successfully!")
	printEmptyLine()

	printf("📁 Key files created in: %s\n", expandedDir)
	printf(indent1+"🔒 Private key: %s.key (encrypted envelope)\n", fileName)
	printf(indent1+"🔑 Public key:  %s.hex (64 hex characters)\n", fileName)
	printf("🔑 Public key hex: %s\n\n", publicKeyHex)

	printWarning("CRITICAL SECURITY REMINDER:")
	printf(indent1+"• Keep %s.key and its passphrase SECRET and secure!\n", fileName)
	printTextLn(indent1 + "• Back up your private key in a safe location ('guardiand key backup')")
	printTextLn(indent1 + "• Without the private key, you cannot decrypt shares sent to you")
	printf(indent1+"• The public key (%s.hex) can be shared safely\n\n", fileName)

	return nil
}

// runCreateEncryptionKey performs the same logic as the create-encryption-key
// command but uses config-derived paths and returns the public key hex for
// config init. The private key is always written as an encrypted envelope —
// there is no plaintext generation path (key custody plan, decision 1).
func runCreateEncryptionKey(manager *config.Manager, passphrase string) (string, error) {
	// Use config-derived paths
	privateKeyPath := manager.GetPrivateKeyPath()
	publicKeyPath := manager.GetPublicKeyPath()
	directory := manager.GetKeyDirectory()

	// Check if keys already exist
	if _, err := os.Stat(privateKeyPath); err == nil {
		return "", errors.Errorf("private key already exists at %s - use existing keys or move them first", privateKeyPath)
	}

	if _, err := os.Stat(publicKeyPath); err == nil {
		return "", errors.Errorf("public key already exists at %s - use existing keys or move them first", publicKeyPath)
	}

	// Ensure directory exists
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", errors.Wrapf(err, "failed to create directory '%s'", directory)
	}

	return generateEncryptedKeypair(privateKeyPath, publicKeyPath, passphrase)
}

// generateEncryptedKeypair generates an X25519 keypair, writing the private
// key as an encrypted envelope and the public key as hex. Returns the public
// key hex.
func generateEncryptedKeypair(privateKeyPath, publicKeyPath, passphrase string) (string, error) {
	if passphrase == "" {
		return "", errors.New("a share-key passphrase is required — generated keys are always encrypted at rest")
	}

	// Generate new keypair using our crypto package
	keypair, err := crypto.GenerateKeypair()
	if err != nil {
		return "", errors.Wrap(err, "keypair generation failed")
	}
	defer custody.Zero(keypair.PrivateKey[:])

	// Save private key as an encrypted envelope (0600, atomic)
	if err := custody.SaveEncryptedShareKey(privateKeyPath, keypair.PrivateKey, passphrase); err != nil {
		return "", errors.Wrap(err, "failed to save private key")
	}

	// Convert public key to hex
	publicKeyHex, err := crypto.BytesToHex(keypair.PublicKey[:])
	if err != nil {
		// Clean up private key on failure
		os.Remove(privateKeyPath)
		return "", errors.Wrap(err, "failed to convert public key to hex")
	}

	// Save public key as hex string
	if err := os.WriteFile(publicKeyPath, []byte(publicKeyHex), 0644); err != nil {
		// Clean up on failure
		os.Remove(privateKeyPath)
		return "", errors.Wrap(err, "failed to save public key")
	}

	return publicKeyHex, nil
}

// writeEncryptionKeyPassphraseFile stores the share-key passphrase in the
// conventional sibling file of the private key (verbatim, 0600) and records
// the path in config — the daemon must decrypt unattended, and the config
// field carries a file path by design, never the secret itself.
func writeEncryptionKeyPassphraseFile(manager *config.Manager, passphrase string) error {
	sibling := custody.SiblingPassphrasePath(manager.GetPrivateKeyPath())
	if err := custody.WritePassphraseFile(sibling, passphrase); err != nil {
		return err
	}
	return manager.SetWithoutValidation("encryption_key_passphrase", sibling)
}

// promptNewPassphrase reads and confirms a new passphrase with hidden input,
// re-prompting until non-empty and matching.
func promptNewPassphrase(purpose string) (string, error) {
	for {
		printText(indent1 + "🔑 Choose a passphrase for the " + purpose + ": ")
		first, err := readPasswordInput()
		if err != nil {
			return "", err
		}
		passphrase := strings.TrimSpace(string(first))
		if passphrase == "" {
			printWarning("Passphrase cannot be empty")
			continue
		}
		printText(indent1 + "🔁 Confirm passphrase: ")
		second, err := readPasswordInput()
		if err != nil {
			return "", err
		}
		if passphrase != strings.TrimSpace(string(second)) {
			printWarning("Passphrases do not match — try again")
			continue
		}
		return passphrase, nil
	}
}
