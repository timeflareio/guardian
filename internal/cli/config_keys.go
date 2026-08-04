package cli

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/custody"
)

// The share-encryption key material behind the configuration:
// `create-encryption-key` generates a fresh encrypted keypair, and
// `migrate-key` upgrades a legacy plaintext key file to the same envelope.

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
  guardianctl config create-encryption-key

  # Create keys non-interactively
  guardianctl config create-encryption-key --passphrase-file /secure/kek

  # Create keys with custom filename
  guardianctl config create-encryption-key --file-name my-guardian

  # Create keys in custom directory
  guardianctl config create-encryption-key --directory /path/to/keys`,
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
  guardianctl config migrate-key

  # Non-interactive migration for automated fleets
  guardianctl config migrate-key --passphrase-file /secure/kek --accept`,
		RunE: runConfigMigrateKey,
	}

	cmd.Flags().String("passphrase-file", "", "File containing the encryption passphrase (default: configured file, then interactive prompt)")
	cmd.Flags().Bool("accept", false, "skip the backup confirmation prompt")
	cmd.Flags().Bool("no-passphrase-file", false, "do not write a passphrase file for a prompted passphrase (the daemon will need one supplied another way)")

	return cmd
}

func runConfigMigrateKey(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
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
		u.Success("Private key at %s is already encrypted — nothing to do\n", keyPath)
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
			u.Note("Using existing passphrase file: %s", path)
		} else {
			passphraseFromFile = false
			passphrase, err = u.NewPassphrase("share-encryption private key")
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
		u.Warning("This encrypts %s in place. If the passphrase is lost, the key is unrecoverable.", keyPath)
		if !u.Confirm("Confirm you hold an independent backup (guardianctl key backup, or the 24-word mnemonic)") {
			u.Warning("Migration cancelled — run 'guardianctl key backup' first.")
			u.EmptyLine()
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
		u.Note("Passphrase stored at %s (0600) so the daemon can decrypt unattended", custody.SiblingPassphrasePath(keyPath))
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

	u.Success("Private key at %s is now encrypted at rest ✓\n", keyPath)
	return nil
}

func runConfigCreateEncryptionKey(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	manager, _, err := optionalConfig(cmd)
	if err != nil {
		return err
	}

	// Get flag values
	fileName, _ := cmd.Flags().GetString("file-name")
	directory, _ := cmd.Flags().GetString("directory")

	// Determine directory - use flag if provided, otherwise derive from config
	// path. The flag is expanded by the same rules as a path-tagged field.
	expandedDir := manager.GetKeyDirectory()
	if directory != "" {
		expandedDir = config.ExpandPath(directory)
	}

	// Define file paths
	privateKeyPath := filepath.Join(expandedDir, fileName+".key")
	publicKeyPath := filepath.Join(expandedDir, fileName+".hex")

	// Show header
	u.EmptyLine()
	u.Separator("🔑 Creating Encryption Keys")
	u.EmptyLine()

	// Check if keys already exist
	if _, err := os.Stat(privateKeyPath); err == nil {
		u.Error("Private key already exists: %s", privateKeyPath)
		u.Note("To generate new keys, first move the existing keys:")
		u.Printf(ui.Indent1+"mv %s %s.backup\n", privateKeyPath, privateKeyPath)
		u.Printf(ui.Indent1+"mv %s %s.backup\n\n", publicKeyPath, publicKeyPath)
		return errors.New("encryption keys already exist - will not overwrite")
	}

	if _, err := os.Stat(publicKeyPath); err == nil {
		u.Error("Public key already exists: %s", publicKeyPath)
		u.Note("To generate new keys, first move the existing keys:")
		u.Printf(ui.Indent1+"mv %s %s.backup\n", privateKeyPath, privateKeyPath)
		u.Printf(ui.Indent1+"mv %s %s.backup\n\n", publicKeyPath, publicKeyPath)
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
		passphrase, err = u.NewPassphrase("private key")
		if err != nil {
			return errors.Wrap(err, "failed to read passphrase")
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(expandedDir, 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory '%s'", expandedDir)
	}

	u.Printf("📁 Directory: %s\n", expandedDir)
	u.Printf("🔑 Key name:  %s\n\n", fileName)
	u.TextLn("⚡ Generating encryption keys...")

	publicKeyHex, err := generateEncryptedKeypair(privateKeyPath, publicKeyPath, passphrase)
	if err != nil {
		u.Error("Key generation failed: %v", err)
		return err
	}

	// Success summary
	u.EmptyLine()
	u.Separator("Encryption Keys Created Successfully!")
	u.EmptyLine()

	u.Printf("📁 Key files created in: %s\n", expandedDir)
	u.Printf(ui.Indent1+"🔒 Private key: %s.key (encrypted envelope)\n", fileName)
	u.Printf(ui.Indent1+"🔑 Public key:  %s.hex (64 hex characters)\n", fileName)
	u.Printf("🔑 Public key hex: %s\n\n", publicKeyHex)

	u.Warning("CRITICAL SECURITY REMINDER:")
	u.Printf(ui.Indent1+"• Keep %s.key and its passphrase SECRET and secure!\n", fileName)
	u.TextLn(ui.Indent1 + "• Back up your private key in a safe location ('guardianctl key backup')")
	u.TextLn(ui.Indent1 + "• Without the private key, you cannot decrypt shares sent to you")
	u.Printf(ui.Indent1+"• The public key (%s.hex) can be shared safely\n\n", fileName)

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
