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
//
// It runs in two halves. Each step *collects* a value — from a flag, or a
// prompt — and returns it; nothing is written until every step has succeeded,
// and then applyInitSettings writes the lot in one place. A step that fails
// therefore leaves nothing half-applied behind it.

// NewConfigInitCmd creates the config init command
func NewConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration file with defaults",
		Long: `Initialize configuration file with default values.

Creates ~/.timeflare/guardian/config.yaml with sensible defaults if it doesn't exist.
If the file already exists, this command will not overwrite it.

The network is chosen from the list the chain publishes, so the chain id and both
endpoints are set together rather than typed. That is worth insisting on: a wrong
chain id leaves every query working and every transaction failing, which looks
like a healthy guardian until it has already missed a reveal window. Naming no
network takes whatever the published list calls its default; --network custom
configures the endpoints yourself, and is also how a host with no route to the
list completes setup unattended.

The list is read here and nowhere else — what this command writes is what the
daemon runs on, so nothing at runtime depends on reaching it.

Critical parameters can be set via flags or will be prompted interactively.
--non-interactive demands the flag form and names whatever is missing rather
than prompting for it.`,
		Example: `  # Initialize with interactive prompts
  guardianctl config init

  # Initialize for a named network
  guardianctl config init --network devnet

  # Configure the endpoints yourself, reading no published list
  guardianctl config init --network custom

  # Initialize with flags to skip all prompts
  guardianctl config init --non-interactive \
    --key-name [key-name] \
    --keyring-passphrase [password] \
    --encryption-public-key [64-char-hex]

  # Initialize with auto-generated keys (encrypted at rest by default)
  guardianctl config init --non-interactive \
    --key-name [key-name] \
    --keyring-passphrase [password] \
    --encryption-key-passphrase [password] \
    --auto-generate-key

  # Initialize with custom keyring directory (for isolated setups)
  guardianctl config init --non-interactive \
    --key-name [key-name] \
    --keyring-dir [/path/to/keyring] \
    --keyring-passphrase [password] \
    --encryption-key-passphrase [password] \
    --auto-generate-key`,
		RunE: runConfigInit,
	}

	// Add flags for critical parameters
	cmd.Flags().String("network", "", "network id from the chain's published registry (default: whatever it names as its default; \"custom\" configures the endpoints yourself)")
	cmd.Flags().String("encryption-public-key", "", "encryption public key (64 hex chars) - skips interactive prompt")
	cmd.Flags().Bool("auto-generate-key", false, "automatically generate encryption keys instead of prompting - skips interactive prompt")
	cmd.Flags().String("key-name", "", "timeflared keyring key name (used as guardian identifier) - skips interactive prompt")
	cmd.Flags().String("keyring-backend", "file", "keyring backend type (file, os, test, memory)")
	cmd.Flags().String("keyring-dir", "", "directory for the timeflared keyring")
	cmd.Flags().String("keyring-passphrase", "", "keyring passphrase for automated access (stored verbatim in a 0600 file)")
	cmd.Flags().String("encryption-key-passphrase", "", "passphrase encrypting the generated share key at rest (required with --auto-generate-key; stored verbatim in a 0600 file beside the key, never in the config values)")
	cmd.Flags().Bool("non-interactive", false, "never prompt: take every value from flags and fail naming any that are missing")

	return cmd
}

// initFlags is what this invocation asked for, read once so no step has to
// reach back into cobra.
type initFlags struct {
	network           string
	encryptionKey     string
	autoGenerateKey   bool
	keyName           string
	keyringBackend    string
	keyringDir        string
	keyringPassphrase string
	encKeyPassphrase  string
	nonInteractive    bool
}

func readInitFlags(cmd *cobra.Command) initFlags {
	f := initFlags{}
	f.network, _ = cmd.Flags().GetString("network")
	f.encryptionKey, _ = cmd.Flags().GetString("encryption-public-key")
	f.autoGenerateKey, _ = cmd.Flags().GetBool("auto-generate-key")
	f.keyName, _ = cmd.Flags().GetString("key-name")
	f.keyringBackend, _ = cmd.Flags().GetString("keyring-backend")
	f.keyringDir, _ = cmd.Flags().GetString("keyring-dir")
	f.keyringPassphrase, _ = cmd.Flags().GetString("keyring-passphrase")
	f.encKeyPassphrase, _ = cmd.Flags().GetString("encryption-key-passphrase")
	f.nonInteractive, _ = cmd.Flags().GetBool("non-interactive")
	return f
}

// interactive reports whether the wizard prompts.
//
// --non-interactive says so outright. Naming any of the identity flags means it
// too, which is what scripts written before the flag rely on. --keyring-backend
// and --keyring-dir are absent from that set deliberately: neither identifies a
// guardian on its own.
func (f initFlags) interactive() bool {
	if f.nonInteractive {
		return false
	}
	named := f.keyName != "" || f.encryptionKey != "" || f.autoGenerateKey ||
		f.keyringPassphrase != "" || f.encKeyPassphrase != ""
	return !named
}

// backend returns the keyring backend, defaulting to file.
func (f initFlags) backend() string {
	if f.keyringBackend == "" {
		return "file"
	}
	return f.keyringBackend
}

// validateUnattended checks that a run which will never prompt has been given
// everything it needs, naming all of what is missing rather than failing on the
// first one.
func (f initFlags) validateUnattended() error {
	if f.encryptionKey != "" && f.autoGenerateKey {
		return errors.New("cannot use both --encryption-public-key and --auto-generate-key flags together")
	}

	missing := []string{}
	if f.keyName == "" {
		missing = append(missing, "--key-name")
	}
	if f.encryptionKey == "" && !f.autoGenerateKey {
		missing = append(missing, "--encryption-public-key or --auto-generate-key")
	}
	if f.backend() == "file" && f.keyringPassphrase == "" {
		missing = append(missing, "--keyring-passphrase")
	}
	// Generated keys are encrypted at rest by default — there is no plaintext
	// generation path (key custody plan, decision 1).
	if f.autoGenerateKey && f.encKeyPassphrase == "" {
		missing = append(missing, "--encryption-key-passphrase")
	}

	if len(missing) > 0 {
		return errors.Errorf("when using flags for atomic setup, all required flags must be provided. Missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

// initSettings is everything the steps collected, ready to be written.
type initSettings struct {
	network             networkChoice
	keyName             string
	keyringBackend      string
	keyringDir          string
	keyringPassphrase   string
	guardianAddress     string
	encryptionPublicKey string
}

func runConfigInit(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	// Resolve the target path once, up front, so the process never holds two
	// views of the configuration that could disagree.
	manager := newManager(cmd)
	configPath := manager.GetConfigPath()

	if _, err := os.Stat(configPath); err == nil {
		u.TextLn("Configuration file already exists at: " + configPath)
		u.TextLn("Use 'guardianctl config list' to view current settings.")
		return nil
	}

	flags := readInitFlags(cmd)
	interactive := flags.interactive()
	if !interactive {
		if err := flags.validateUnattended(); err != nil {
			return err
		}
	}

	settings := initSettings{
		keyringBackend: flags.backend(),
		keyringDir:     flags.keyringDir,
	}

	if interactive {
		u.EmptyLine()
		u.Header("      🚀 Guardian Configuration Setup")
		u.Separator("      ═══════════════════════════════")
		u.Note("         Press Ctrl+C to exit at any time\n")
	}

	var err error
	// First, because it is the widest-reaching answer and the one an operator
	// arrives already knowing.
	if settings.network, err = collectNetwork(u, flags, interactive); err != nil {
		return err
	}
	if settings.keyName, err = collectSigningKeyName(u, flags); err != nil {
		return err
	}
	if settings.keyringPassphrase, err = collectKeyringPassphrase(u, flags, settings.keyringBackend, interactive); err != nil {
		return err
	}
	if settings.guardianAddress, err = resolveInitAddress(u, manager, flags, settings, interactive); err != nil {
		return err
	}
	if settings.encryptionPublicKey, err = collectEncryptionKey(u, manager, flags, interactive); err != nil {
		return err
	}

	if err := applyInitSettings(manager, settings); err != nil {
		return err
	}
	if err := manager.Save(); err != nil {
		return errors.Wrap(err, "failed to initialize config")
	}

	reportInitSummary(u, manager, configPath, settings)
	return nil
}

// collectSigningKeyName resolves the keyring key name, which doubles as the
// guardian's identifier. An unattended run never prompts: --key-name is
// required by validateUnattended before this is reached.
func collectSigningKeyName(u *ui.Printer, flags initFlags) (string, error) {
	if flags.keyName != "" {
		return flags.keyName, nil
	}

	u.Step("🔑 Step 2: Blockchain Signing Key")
	u.TextLn(ui.Indent1 + "Guardians need a signing key for blockchain transactions (registration, reveals, etc.)")
	u.TextLn(ui.Indent1 + "This key will also serve as your guardian identifier.\n")

	u.Note(ui.Indent1+"Create the signing key with guardianctl (using the %s keyring):\n", flags.backend())

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

	keyName := u.PromptInput("🗝️  Enter keyring key name: ")
	if keyName == "" {
		return "", errors.New("keyring key name is required")
	}
	u.Note(ui.Indent1 + "─────────────────────────────────────────────────\n")
	return keyName, nil
}

// collectKeyringPassphrase resolves the keyring passphrase, which is what lets
// the daemon sign confirmations and reveals unattended. Empty is a valid
// outcome: only the file backend needs one, and an operator may decline.
func collectKeyringPassphrase(u *ui.Printer, flags initFlags, backend string, interactive bool) (string, error) {
	if flags.keyringPassphrase != "" {
		return flags.keyringPassphrase, nil
	}
	if backend != "file" || !interactive {
		return "", nil
	}

	u.EmptyLine()
	u.Step("🔐 Step 3: Keyring Passphrase Setup")
	u.TextLn(ui.Indent1 + "Guardian operations require automatic transaction signing for confirming")
	u.TextLn(ui.Indent1 + "and revealing secret shares. A passphrase file enables these automated")
	u.TextLn(ui.Indent1 + "transactions without manual intervention.\n")

	u.Text(ui.Indent1 + "🔑 Enter your keyring passphrase: ")
	passphraseBytes, err := u.ReadPassword()
	if err != nil {
		u.Warning("Could not read passphrase: %v", err)
		return "", nil
	}
	passphrase := strings.TrimSpace(string(passphraseBytes))
	if passphrase != "" {
		u.Success("Passphrase collected")
	}
	return passphrase, nil
}

// resolveInitAddress derives the guardian's address from the signing key, which
// needs the passphrase to open the keyring. An unattended run treats failure as
// fatal: the address it would have written is the guardian's identity on chain.
func resolveInitAddress(u *ui.Printer, manager *config.Manager, flags initFlags, s initSettings, interactive bool) (string, error) {
	if s.keyringPassphrase == "" {
		return "", nil
	}
	if interactive {
		u.Step(ui.Indent1 + "🔍 Resolving guardian address from key...")
	}

	address := resolveAddressWithPassword(manager, s.keyName, s.keyringBackend, s.keyringPassphrase, flags.keyringDir)
	switch {
	case !interactive && address == "":
		return "", errors.Errorf("failed to resolve guardian address from key-name '%s' with provided passphrase - please verify the key exists and passphrase is correct", s.keyName)
	case !interactive:
		u.Success("Guardian address resolved: %s", address)
	case address != "":
		u.Success("Guardian address: %s", address)
		u.Note(ui.Indent1 + "─────────────────────────────────────────────────")
	default:
		u.Warning("Could not resolve guardian address")
		u.Note(ui.Indent1 + "─────────────────────────────────────────────────")
	}
	return address, nil
}

// collectEncryptionKey resolves the share-encryption public key, generating the
// keypair behind it when asked. Interactively, a generation failure or a
// malformed key is reported and the wizard continues without one — the operator
// is told how to set it later, which beats losing the rest of the setup.
func collectEncryptionKey(u *ui.Printer, manager *config.Manager, flags initFlags, interactive bool) (string, error) {
	switch {
	case flags.encryptionKey != "":
		return flags.encryptionKey, nil

	case flags.autoGenerateKey:
		publicKey, err := runCreateEncryptionKey(manager, flags.encKeyPassphrase)
		if err != nil {
			return "", errors.Wrap(err, "auto key generation failed")
		}
		if err := writeEncryptionKeyPassphraseFile(manager, flags.encKeyPassphrase); err != nil {
			return "", errors.Wrap(err, "failed to store share-key passphrase file")
		}
		return publicKey, nil

	case !interactive:
		// Unreachable: validateUnattended requires one of the two above.
		return "", errors.New("encryption key required: use --encryption-public-key or --auto-generate-key")
	}

	u.EmptyLine()
	u.Step("🔑 Step 4: Encryption Key Setup")
	u.TextLn(ui.Indent1 + "Guardians need encryption keys to securely receive and decrypt secret shares.")
	u.TextLn(ui.Indent1 + "You can either provide an existing public key or generate new keys.\n")

	u.Note(ui.Indent1 + "Options:")
	u.TextLn(ui.Indent2 + "1. Generate new keys automatically (recommended for new guardians)")
	u.TextLn(ui.Indent2 + "2. Provide existing public key (if you already have encryption keys)\n")

	choice := strings.ToLower(strings.TrimSpace(
		u.PromptInput(ui.Indent1 + "🔀 Generate new keys? [Y/n]: ")))

	var publicKey string
	if choice == "" || choice == "y" || choice == "yes" {
		var err error
		if publicKey, err = generateKeyInteractively(u, manager); err != nil {
			return "", err
		}
	} else {
		publicKey = u.PromptInput("\n" + ui.Indent1 + "🔑 Enter your encryption public key (64 hex characters): ")
		if len(publicKey) != 64 {
			u.Warning("Invalid key length: expected 64 characters, got %d", len(publicKey))
			u.Note(ui.Indent1 + "You can set the correct encryption key later with:" + ui.Indent1)
			u.Command("guardianctl config set encryption-public-key <64-hex-chars>\n\n")
			publicKey = ""
		} else {
			u.Success("Using provided encryption key: %s...\n", publicKey[:8])
		}
	}

	if publicKey != "" {
		u.Note(ui.Indent1 + "─────────────────────────────────────────────────\n")
	}
	return publicKey, nil
}

// generateKeyInteractively generates the share keypair, encrypted at rest, and
// reports where every piece of it landed. "" means generation failed and the
// operator was told how to set the key later.
func generateKeyInteractively(u *ui.Printer, manager *config.Manager) (string, error) {
	u.EmptyLine()
	u.Note(ui.Indent1 + "The private key is stored encrypted at rest. Choose a passphrase;")
	u.Note(ui.Indent1 + "it is kept in a 0600 file beside the key so the daemon can run unattended.")
	passphrase, err := u.NewPassphrase("share-encryption private key")
	if err != nil {
		return "", errors.Wrap(err, "failed to read share-key passphrase")
	}

	u.TextLn("\n" + ui.Indent1 + "⚡ Generating encryption keys...")

	publicKey, err := runCreateEncryptionKey(manager, passphrase)
	if err != nil {
		u.Warning("Key generation failed: %v", err)
		u.Note(ui.Indent1 + "You can set the encryption key later with:" + ui.Indent1)
		u.Command("guardianctl config set encryption-public-key <64-hex-chars>\n\n")
		return "", nil
	}
	if err := writeEncryptionKeyPassphraseFile(manager, passphrase); err != nil {
		return "", errors.Wrap(err, "failed to store share-key passphrase file")
	}

	u.Success("Encryption keys generated successfully!")
	privateKeyPath := manager.GetPrivateKeyPath()
	u.TextLn(ui.Indent1 + "📁 Key locations:")
	u.TextLn(ui.Indent2 + "• Private key: " + privateKeyPath + " (encrypted at rest — keep it SECRET!)")
	u.TextLn(ui.Indent2 + "• Passphrase:  " + custody.SiblingPassphrasePath(privateKeyPath))
	u.TextLn(ui.Indent2 + "• Public key:  " + manager.GetPublicKeyPath())
	u.TextLn(ui.Indent2 + "• Public key hex: " + publicKey)
	u.Warning("CRITICAL: Back up your private key securely!")
	u.TextLn(ui.Indent2 + "• Run 'guardianctl key backup' after registration for a portable encrypted bundle")
	u.TextLn(ui.Indent2 + "• Without the key, you cannot decrypt shares sent to you")
	u.TextLn(ui.Indent2 + "• Lost keys prevent participation in reveals, resulting in slashing penalties\n")
	return publicKey, nil
}

// applyInitSettings writes everything the steps collected. This is the only
// place init touches the configuration.
func applyInitSettings(manager *config.Manager, s initSettings) error {
	values := map[string]string{
		// The keyring key name doubles as the guardian's identifier.
		"guardian_id":     s.keyName,
		"key_name":        s.keyName,
		"keyring_backend": s.keyringBackend,
	}
	// Recording which network the endpoints came from is what lets `config
	// doctor` tell a deliberate override from an endpoint that moved underneath
	// the guardian.
	if s.network.selected() {
		values["network"] = s.network.id
	}
	if s.network.chainID != "" {
		values["chain_id"] = s.network.chainID
	}
	if s.network.rpcEndpoint != "" {
		values["rpc_endpoint"] = s.network.rpcEndpoint
	}
	if s.network.grpcEndpoint != "" {
		values["grpc_endpoint"] = s.network.grpcEndpoint
	}
	// The fallback poll rate follows the network's cadence: polling much faster
	// than blocks arrive is load for nothing, and much slower risks missing a
	// window. Derived here, from the registry, and not stored as a cadence — the
	// daemon needs the interval, not the number it came from.
	if s.network.blockTime > 0 {
		values["polling_interval"] = s.network.blockTime.String()
	}
	if s.network.grpcTLS {
		values["grpc_tls"] = "true"
	}
	if s.encryptionPublicKey != "" {
		values["encryption_public_key"] = s.encryptionPublicKey
		values["encryption_private_key_path"] = manager.GetPrivateKeyPath()
	}
	if s.keyringDir != "" {
		values["keyring_dir"] = s.keyringDir
	}
	if s.guardianAddress != "" {
		values["guardian_address"] = s.guardianAddress
	}

	for key, value := range values {
		if err := manager.Set(key, value); err != nil {
			return errors.Wrapf(err, "failed to set %s", key)
		}
	}

	if s.keyringPassphrase == "" {
		return nil
	}
	if err := manager.EnsureDirectoriesExist(); err != nil {
		return errors.Wrap(err, "failed to create guardian directory")
	}
	passphraseFile := filepath.Join(manager.GetKeyDirectory(), "keyring_passphrase")
	if err := custody.WritePassphraseFile(passphraseFile, s.keyringPassphrase); err != nil {
		return errors.Wrap(err, "failed to create passphrase file")
	}
	if err := manager.Set("keyring_passphrase", passphraseFile); err != nil {
		return errors.Wrap(err, "failed to set keyring passphrase file")
	}
	return nil
}

// effectiveChainID and effectiveGRPCEndpoint report what was actually written:
// the selected value, or the default the manual path left standing.
func effectiveChainID(s initSettings, manager *config.Manager) string {
	if s.network.chainID != "" {
		return s.network.chainID
	}
	return manager.GetConfig().ChainID
}

func effectiveGRPCEndpoint(s initSettings, manager *config.Manager) string {
	if s.network.grpcEndpoint != "" {
		return s.network.grpcEndpoint
	}
	return manager.GetConfig().GRPCEndpoint
}

// reportInitSummary says what was written and what to do next.
func reportInitSummary(u *ui.Printer, manager *config.Manager, configPath string, s initSettings) {
	u.EmptyLine()
	u.Separator("    Configuration Initialized Successfully")
	u.EmptyLine()

	u.Step("📋 Configuration Summary:")
	u.Text(ui.Indent1 + "📁 Config File:           ")
	u.Path("%s\n", configPath)
	if s.network.selected() {
		u.Text(ui.Indent1 + "🌐 Network:               ")
		u.Value("%s", s.network.id)
		u.TextLn("  (%s)", effectiveChainID(s, manager))
		u.Text(ui.Indent1 + "🔌 gRPC Endpoint:         ")
		u.Value("%s", effectiveGRPCEndpoint(s, manager))
		if s.network.grpcTLS {
			u.TextLn("  (TLS)")
		} else {
			u.EmptyLine()
		}
	}
	u.Text(ui.Indent1 + "🗝️  Guardian Identity:      ")
	u.Value("%s\n", s.keyName)
	u.Text(ui.Indent1 + "🔐 Keyring Backend:        ")
	u.Value("%s\n", s.keyringBackend)
	u.Text(ui.Indent1 + "🔑 Encryption Public Key:  ")
	if s.encryptionPublicKey != "" {
		u.Value("%s\n", s.encryptionPublicKey)
		u.Text(ui.Indent1 + "🔒 Encryption Private Key: ")
		u.Path("%s\n", manager.GetPrivateKeyPath())
	} else {
		u.Note("(empty - set later with 'guardianctl config set encryption-public-key <key>')")
	}

	u.Step("🚀 Next Steps:")
	u.Text(ui.Indent1 + "• ")
	u.Command("guardianctl config list")
	u.TextLn(" - view all settings")

	u.Text(ui.Indent1 + "• ")
	u.Command("guardianctl register")
	u.TextLn(" - register your guardian with the network\n")
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
