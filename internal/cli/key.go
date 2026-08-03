package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/custody"
)

// NewKeyCmd creates the key command group (backup & restore as a first-class
// flow — guardian key custody plan, Phase 2).
func NewKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Back up and restore the guardian's keys",
		Long: `Back up and restore the guardian's keys.

A guardian's economic life depends on two keys:
  1. The X25519 share-encryption keyring — the current epoch's key plus any
     retired epochs still serving in-flight assignments ('guardiand
     rotate-key' rotates forward; each epoch's binding is immutable). Losing
     an epoch's key means missing every reveal encrypted to it (no-reveal
     slash on each).
  2. The Cosmos signing key in the keyring — needed to sign confirmations
     and reveals.

'key backup' exports the whole epoch keyring and the signing keyring, plus
identity context, as one passphrase-encrypted bundle. 'key restore' reverses
it and verifies the restored key against the registered on-chain guardian
record before declaring success.`,
	}

	cmd.AddCommand(NewKeyBackupCmd())
	cmd.AddCommand(NewKeyRestoreCmd())

	return cmd
}

// NewKeyBackupCmd creates the key backup command.
func NewKeyBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Export an encrypted backup bundle of the guardian's keys",
		Long: `Export an encrypted backup bundle containing the share-encryption private
key, the signing keyring files, and a fingerprint of the current configuration.

The bundle is encrypted under a backup passphrase of its own (this is NOT the
at-rest key passphrase — the bundle travels to off-host storage, so it gets an
independent secret). Store the bundle and its passphrase separately.

--show-mnemonic additionally prints the 24-word recovery phrase for the share
key (BIP39 over the raw key bytes) — the human-typable fallback when no bundle
is reachable. Treat the words exactly like the key itself.`,
		Example: `  # Interactive backup (prompts for a backup passphrase)
  guardiand key backup

  # Non-interactive, explicit output path
  guardiand key backup --output /secure/guardian.tfb --passphrase-file /secure/backup-pass

  # Also print the 24-word recovery phrase
  guardiand key backup --show-mnemonic`,
		RunE:         runKeyBackup,
		SilenceUsage: true,
	}

	cmd.Flags().String("output", "", "output path for the encrypted bundle (default: guardian-backup-<key-name>-<date>.tfb)")
	cmd.Flags().String("passphrase-file", "", "file containing the backup passphrase (default: interactive prompt)")
	cmd.Flags().Bool("show-mnemonic", false, "print the 24-word recovery phrase for the share key")

	return cmd
}

func runKeyBackup(cmd *cobra.Command, args []string) error {
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}
	effective := manager.GetConfig()

	output, _ := cmd.Flags().GetString("output")
	passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
	showMnemonic, _ := cmd.Flags().GetBool("show-mnemonic")

	// Load the share key (decrypts via the configured/sibling passphrase file)
	privateKey, err := effective.GetEncryptionPrivateKey()
	if err != nil {
		return errors.Wrap(err, "cannot back up: share key not loadable")
	}
	defer custody.Zero(privateKey[:])

	derived, err := crypto.DerivePublicKey(privateKey)
	if err != nil {
		return errors.Wrap(err, "failed to derive public key")
	}
	derivedHex := hex.EncodeToString(derived[:])
	if effective.EncryptionPublicKey != "" && effective.EncryptionPublicKey != derivedHex {
		return errors.Errorf(
			"configured encryption_public_key (%s) does not match the key file's derived public key (%s) — fix the configuration before backing up",
			effective.EncryptionPublicKey, derivedHex)
	}

	keyringFiles, err := custody.CollectKeyringFiles(effective.KeyringDir)
	if err != nil {
		return err
	}

	// The whole epoch keyring travels in one bundle: retired epoch keys
	// (<key>.epoch<N>, present until their last assignment settles) ride
	// alongside the current key so a restore can serve in-flight
	// assignments encrypted to any epoch.
	retiredKeys, err := custody.CollectRetiredKeys(effective.EncryptionPrivateKeyPath,
		custody.FilePassphrase(effective.EncryptionKeyPassphrase, effective.EncryptionPrivateKeyPath))
	if err != nil {
		return errors.Wrap(err, "cannot back up: a retired epoch key failed to load")
	}
	currentEpoch := uint64(0)
	for epoch := range retiredKeys {
		if epoch+1 > currentEpoch {
			currentEpoch = epoch + 1
		}
	}

	fingerprint := ""
	if configBytes, err := os.ReadFile(manager.GetConfigPath()); err == nil {
		sum := sha256.Sum256(configBytes)
		fingerprint = hex.EncodeToString(sum[:])
	}

	bundle := &custody.Bundle{
		Version:             custody.BundleVersion,
		CreatedAt:           time.Now().UTC(),
		ChainID:             effective.ChainID,
		KeyName:             effective.KeyName,
		GuardianAddress:     effective.GuardianAddress,
		EncryptionPublicKey: derivedHex,
		SharePrivateKey:     privateKey[:],
		RetiredKeys:         retiredKeys,
		CurrentKeyEpoch:     currentEpoch,
		KeyringFiles:        keyringFiles,
		ConfigFingerprint:   fingerprint,
	}

	// Backup passphrase — independent of the at-rest key passphrase
	var passphrase string
	if passphraseFile != "" {
		passphrase, err = custody.ReadPassphraseFile(passphraseFile)
		if err != nil {
			return err
		}
	} else {
		printEmptyLine()
		printNote("Choose a backup passphrase. It protects the bundle wherever it is stored")
		printNote("and is independent of the at-rest key passphrase. Store them separately.")
		passphrase, err = promptNewPassphrase("backup bundle")
		if err != nil {
			return err
		}
	}

	blob, err := custody.SealBundle(bundle, passphrase)
	if err != nil {
		return err
	}

	if output == "" {
		output = fmt.Sprintf("guardian-backup-%s-%s.tfb", effective.KeyName, time.Now().UTC().Format("20060102"))
	}
	if err := os.WriteFile(output, blob, 0600); err != nil {
		return errors.Wrap(err, "failed to write backup bundle")
	}

	printEmptyLine()
	printSeparator("Guardian Backup Created")
	printEmptyLine()
	printf(indent1+"📦 Bundle:        %s\n", output)
	printf(indent1+"🗝️  Key name:      %s\n", effective.KeyName)
	printf(indent1+"🔑 Public key:    %s\n", derivedHex)
	printf(indent1+"🗂  Keyring files: %d\n", len(keyringFiles))
	if len(retiredKeys) > 0 {
		printf(indent1+"🔁 Retired epoch keys: %d (serving in-flight assignments until settlement)\n", len(retiredKeys))
	}
	printEmptyLine()
	printNote("Storage guidance:")
	printTextLn(indent1 + "• Copy the bundle OFF this host (the point is surviving disk loss)")
	printTextLn(indent1 + "• Store the backup passphrase separately from the bundle")
	printTextLn(indent1 + "• Re-run after every signing-key change; drill 'key restore' before it matters")
	printEmptyLine()

	if showMnemonic {
		mnemonic, err := custody.KeyToMnemonic(privateKey)
		if err != nil {
			return err
		}
		printWarning("The 24 words below ARE the share key. Anyone holding them can decrypt")
		printWarning("every share ever assigned to this guardian. Write them down; never store")
		printWarning("them digitally in plaintext.")
		printEmptyLine()
		printTextLn(indent1 + mnemonic)
		printEmptyLine()
	}

	return nil
}

// NewKeyRestoreCmd creates the key restore command.
func NewKeyRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore the guardian's keys from a backup bundle or mnemonic",
		Long: `Restore the guardian's keys from an encrypted backup bundle (or, with
--from-mnemonic, reconstruct the share key alone from its 24-word recovery
phrase).

Before declaring success the restored share key is verified against the chain:
its derived public key must match the registered guardian record. Pass
--offline to skip that check when no chain is reachable — the startup
self-check will still enforce it on 'guardiand start'.

The share key is written encrypted at rest (the at-rest passphrase is resolved
from the configuration, or prompted for and stored beside the key). Keyring
files from a bundle are restored into the configured keyring directory.`,
		Example: `  # Restore from a bundle (prompts for the backup passphrase)
  guardiand key restore --input /secure/guardian.tfb

  # Restore on an unreachable-chain host
  guardiand key restore --input /secure/guardian.tfb --offline

  # Reconstruct the share key from the 24 words (no keyring restore)
  guardiand key restore --from-mnemonic`,
		RunE:         runKeyRestore,
		SilenceUsage: true,
	}

	cmd.Flags().String("input", "", "path to the encrypted backup bundle")
	cmd.Flags().String("passphrase-file", "", "file containing the backup passphrase (default: interactive prompt)")
	cmd.Flags().Bool("from-mnemonic", false, "restore the share key from its 24-word recovery phrase instead of a bundle")
	cmd.Flags().Bool("offline", false, "skip verification against the on-chain guardian record")
	cmd.Flags().Bool("force", false, "overwrite existing key and keyring files")

	return cmd
}

func runKeyRestore(cmd *cobra.Command, args []string) error {
	manager, effective, err := requireConfig(cmd)
	if err != nil {
		return errors.Wrap(err, "a configuration is required before restoring")
	}

	input, _ := cmd.Flags().GetString("input")
	passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
	fromMnemonic, _ := cmd.Flags().GetBool("from-mnemonic")
	offline, _ := cmd.Flags().GetBool("offline")
	force, _ := cmd.Flags().GetBool("force")

	var (
		privateKey [32]byte
		bundle     *custody.Bundle
	)

	switch {
	case fromMnemonic:
		words := promptForInput("🔤 Enter the 24-word recovery phrase: ")
		privateKey, err = custody.KeyFromMnemonic(strings.TrimSpace(words))
		if err != nil {
			return err
		}
	case input != "":
		blob, readErr := os.ReadFile(input)
		if readErr != nil {
			return errors.Wrap(readErr, "failed to read backup bundle")
		}
		var passphrase string
		if passphraseFile != "" {
			passphrase, err = custody.ReadPassphraseFile(passphraseFile)
			if err != nil {
				return err
			}
		} else {
			printText("🔑 Enter the backup passphrase: ")
			raw, readErr := readPasswordInput()
			if readErr != nil {
				return readErr
			}
			passphrase = strings.TrimSpace(string(raw))
		}
		bundle, err = custody.OpenBundle(blob, passphrase)
		if err != nil {
			return err
		}
		copy(privateKey[:], bundle.SharePrivateKey)
		custody.Zero(bundle.SharePrivateKey)
	default:
		return errors.New("either --input <bundle> or --from-mnemonic is required")
	}
	defer custody.Zero(privateKey[:])

	derived, err := crypto.DerivePublicKey(privateKey)
	if err != nil {
		return errors.Wrap(err, "failed to derive public key from restored key")
	}
	derivedHex := hex.EncodeToString(derived[:])

	// Verify against the registered on-chain guardian record.
	guardianAddress := effective.GuardianAddress
	if bundle != nil && bundle.GuardianAddress != "" {
		guardianAddress = bundle.GuardianAddress
	}
	if !offline {
		if guardianAddress == "" {
			return errors.New("no guardian address available for chain verification — set guardian_address in config, or pass --offline")
		}
		record, err := queryGuardianRecord(effective, guardianAddress)
		if err != nil {
			return errors.Wrapf(err, "chain verification failed for %s (pass --offline to restore without it)", guardianAddress)
		}
		if !bytes.Equal(record.EncryptionPublicKey, derived[:]) {
			return errors.Errorf(
				"restored key derives public key %s, but guardian %s is registered with %x — this is NOT the right key for this guardian",
				derivedHex, guardianAddress, record.EncryptionPublicKey)
		}
		printSuccess("Chain verification passed: restored key matches the registered guardian record")
	} else {
		printWarning("Skipping chain verification (--offline) — 'guardiand start' will still enforce it")
	}

	// Write the share key, encrypted at rest.
	keyPath := effective.EncryptionPrivateKeyPath
	if _, err := os.Stat(keyPath); err == nil && !force {
		return errors.Errorf("private key already exists at %s — re-run with --force to overwrite", keyPath)
	}

	var atRestPassphrase string
	if path, resolveErr := custody.ResolvePassphrasePath(effective.EncryptionKeyPassphrase, keyPath); resolveErr == nil {
		atRestPassphrase, err = custody.ReadPassphraseFile(path)
		if err != nil {
			return err
		}
	} else {
		printEmptyLine()
		printNote("No at-rest passphrase file found. Choose one; it is stored in a 0600 file")
		printNote("beside the key so the daemon can decrypt unattended.")
		atRestPassphrase, err = promptNewPassphrase("share-encryption private key")
		if err != nil {
			return err
		}
		if err := custody.WritePassphraseFile(custody.SiblingPassphrasePath(keyPath), atRestPassphrase); err != nil {
			return err
		}
		if err := manager.SetWithoutValidation("encryption_key_passphrase", custody.SiblingPassphrasePath(keyPath)); err != nil {
			return err
		}
	}

	if err := custody.SaveEncryptedShareKey(keyPath, privateKey, atRestPassphrase); err != nil {
		return err
	}

	// Restore retired epoch keys beside the current key — in-flight
	// assignments encrypted under earlier epochs need them until settlement.
	if bundle != nil && len(bundle.RetiredKeys) > 0 {
		if err := custody.RestoreRetiredKeys(keyPath, bundle.RetiredKeys, atRestPassphrase); err != nil {
			return err
		}
		printSuccess("Restored %d retired epoch key(s) beside the current key", len(bundle.RetiredKeys))
	}

	// Restore keyring files and identity fields from the bundle.
	if bundle != nil {
		if len(bundle.KeyringFiles) > 0 {
			if err := custody.RestoreKeyringFiles(effective.KeyringDir, bundle.KeyringFiles, force); err != nil {
				return err
			}
			printSuccess("Restored %d keyring file(s) into %s", len(bundle.KeyringFiles), effective.KeyringDir)
		}
		if bundle.KeyName != "" {
			if err := manager.SetWithoutValidation("key_name", bundle.KeyName); err != nil {
				return err
			}
		}
		if bundle.GuardianAddress != "" {
			if err := manager.SetWithoutValidation("guardian_address", bundle.GuardianAddress); err != nil {
				return err
			}
		}
		if bundle.ConfigFingerprint != "" {
			if configBytes, err := os.ReadFile(manager.GetConfigPath()); err == nil {
				sum := sha256.Sum256(configBytes)
				if hex.EncodeToString(sum[:]) != bundle.ConfigFingerprint {
					printNote("Config fingerprint differs from backup time — review 'guardiand config list' for drift")
				}
			}
		}
	}

	if err := manager.SetWithoutValidation("encryption_public_key", derivedHex); err != nil {
		return err
	}
	if err := manager.Save(); err != nil {
		return errors.Wrap(err, "failed to save config")
	}

	printEmptyLine()
	printSeparator("Guardian Keys Restored")
	printEmptyLine()
	printf(indent1+"🔒 Private key: %s (encrypted at rest)\n", keyPath)
	printf(indent1+"🔑 Public key:  %s\n", derivedHex)
	printEmptyLine()
	printNote("Next steps:")
	printText(indent1 + "• ")
	printCommand("guardiand config doctor")
	printTextLn(" — confirm keys and configuration load")
	printText(indent1 + "• ")
	printCommand("guardiand start")
	printTextLn(" — the startup self-check re-verifies the key against the chain\n")

	return nil
}

// queryGuardianRecord fetches the registered guardian record over gRPC —
// query-only, no keyring involved (restore may run before the keyring
// exists).
func queryGuardianRecord(cfg *config.Config, address string) (*secretstypes.Guardian, error) {
	conn, err := grpc.NewClient(cfg.GRPCEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create gRPC client for %s", cfg.GRPCEndpoint)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancel()

	resp, err := secretstypes.NewQueryClient(conn).Guardian(ctx, &secretstypes.QueryGuardianRequest{Address: address})
	if err != nil {
		return nil, err
	}
	return &resp.Guardian, nil
}
