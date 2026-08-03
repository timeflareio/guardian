package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/blockchain"
	"github.com/timeflareio/guardian/config"
	"github.com/timeflareio/guardian/custody"
)

// NewRotateKeyCmd creates the rotate-key command (guardian key rotation plan:
// generate → backup ceremony → submit — NEVER submit before the backup
// ceremony completes).
func NewRotateKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate the share-encryption key forward for future assignments",
		Long: `Rotate the guardian's share-encryption key forward: a freshly generated key
becomes the next epoch for FUTURE assignments (effective from the next block),
while every existing assignment stays bound to the key it was encrypted to.
The old key is kept beside the new one (<private_key>.epoch<N>) and keeps
serving its in-flight assignments; it is safe to delete once the last of them
settles.

The flow is generate → backup ceremony → submit: a passphrase-encrypted
backup bundle carrying the WHOLE keyring (new key + every retired epoch) is
written and confirmed BEFORE the rotation transaction is broadcast.

On-chain constraints: a flat burned rotation fee (one guardian-day of the
master rate) and a minimum interval of one rotation per 432,000 blocks
(~30 days, measured from the current epoch's effective height). Inside the
window, 'guardiand update --accepting-secrets=false' gives identical forward
protection immediately and free.

Rotation is NOT loss recovery: a lost key still misses every reveal encrypted
to it. If the daemon is running, restart it after rotating (it also detects
the new epoch and reloads, but a restart makes the cutover immediate).`,
		Example: `  # Interactive rotation (prompts for the backup passphrase)
  guardiand rotate-key

  # Non-interactive
  guardiand rotate-key --backup-output /secure/rotation.tfb \
    --backup-passphrase-file /secure/backup-pass --yes`,
		RunE:         runRotateKey,
		SilenceUsage: true,
	}

	cmd.Flags().String("backup-output", "", "output path for the pre-rotation backup bundle (default: guardian-rotation-<key-name>-<date>.tfb)")
	cmd.Flags().String("backup-passphrase-file", "", "file containing the backup passphrase (default: interactive prompt)")
	cmd.Flags().Bool("yes", false, "skip the submission confirmation prompt")

	return cmd
}

func runRotateKey(cmd *cobra.Command, args []string) error {
	if err := cfgManager.Load(); err != nil {
		return err
	}
	effective := cfgManager.GetConfig()

	backupOutput, _ := cmd.Flags().GetString("backup-output")
	backupPassphraseFile, _ := cmd.Flags().GetString("backup-passphrase-file")
	assumeYes, _ := cmd.Flags().GetBool("yes")

	// 1. The current key must load and match the registered guardian record —
	// rotating on top of a wrong local key would bury the mismatch.
	currentKey, err := effective.GetEncryptionPrivateKey()
	if err != nil {
		return errors.Wrap(err, "cannot rotate: current share key not loadable")
	}
	defer custody.Zero(currentKey[:])
	currentDerived, err := crypto.DerivePublicKey(currentKey)
	if err != nil {
		return errors.Wrap(err, "failed to derive the current public key")
	}

	if effective.GuardianAddress == "" {
		return errors.New("guardian_address is not configured")
	}
	record, err := queryGuardianRecord(effective, effective.GuardianAddress)
	if err != nil {
		return errors.Wrap(err, "cannot rotate: failed to fetch the registered guardian record")
	}
	if !bytes.Equal(record.EncryptionPublicKey, currentDerived[:]) {
		return errors.Errorf(
			"local share key derives %x but guardian %s is registered with %x — restore the correct key ('guardiand key restore') before rotating",
			currentDerived[:], effective.GuardianAddress, record.EncryptionPublicKey)
	}
	currentEpoch := record.CurrentKeyEpoch

	// 2. Generate the next epoch's keypair.
	newPair, err := crypto.GenerateKeypair()
	if err != nil {
		return errors.Wrap(err, "failed to generate the new keypair")
	}
	defer custody.Zero(newPair.PrivateKey[:])
	newPubHex := hex.EncodeToString(newPair.PublicKey[:])

	printEmptyLine()
	printSeparator("Guardian Key Rotation")
	printEmptyLine()
	printf(indent1+"👮 Guardian:       %s\n", effective.GuardianAddress)
	printf(indent1+"🔑 Current epoch:  %d (%x)\n", currentEpoch, currentDerived[:])
	printf(indent1+"🆕 Next epoch:     %d (%s)\n", currentEpoch+1, newPubHex)
	printEmptyLine()

	// 3. Backup ceremony — the WHOLE keyring (new key + current, which is
	// about to retire, + already-retired epochs) travels in one bundle,
	// sealed under an independent backup passphrase, BEFORE anything is
	// submitted or rewritten.
	passphraseSupplier := custody.FilePassphrase(effective.EncryptionKeyPassphrase, effective.EncryptionPrivateKeyPath)
	retiredKeys, err := custody.CollectRetiredKeys(effective.EncryptionPrivateKeyPath, passphraseSupplier)
	if err != nil {
		return errors.Wrap(err, "cannot rotate: a retired epoch key failed to load")
	}
	retiredKeys[currentEpoch] = append([]byte(nil), currentKey[:]...)

	keyringFiles, err := custody.CollectKeyringFiles(effective.KeyringDir)
	if err != nil {
		return err
	}
	fingerprint := ""
	if configBytes, err := os.ReadFile(cfgManager.GetConfigPath()); err == nil {
		sum := sha256.Sum256(configBytes)
		fingerprint = hex.EncodeToString(sum[:])
	}
	bundle := &custody.Bundle{
		Version:             custody.BundleVersion,
		CreatedAt:           time.Now().UTC(),
		ChainID:             effective.ChainID,
		KeyName:             effective.KeyName,
		GuardianAddress:     effective.GuardianAddress,
		EncryptionPublicKey: newPubHex,
		SharePrivateKey:     newPair.PrivateKey[:],
		RetiredKeys:         retiredKeys,
		CurrentKeyEpoch:     currentEpoch + 1,
		KeyringFiles:        keyringFiles,
		ConfigFingerprint:   fingerprint,
	}

	var backupPassphrase string
	if backupPassphraseFile != "" {
		backupPassphrase, err = custody.ReadPassphraseFile(backupPassphraseFile)
		if err != nil {
			return err
		}
	} else {
		printNote("Backup ceremony: choose a backup passphrase for the rotation bundle.")
		printNote("It carries the NEW key and every old epoch — store it off this host.")
		backupPassphrase, err = promptNewPassphrase("rotation backup bundle")
		if err != nil {
			return err
		}
	}
	blob, err := custody.SealBundle(bundle, backupPassphrase)
	if err != nil {
		return err
	}
	if backupOutput == "" {
		backupOutput = fmt.Sprintf("guardian-rotation-%s-%s.tfb", effective.KeyName, time.Now().UTC().Format("20060102"))
	}
	if err := os.WriteFile(backupOutput, blob, 0600); err != nil {
		return errors.Wrap(err, "failed to write the rotation backup bundle")
	}
	printSuccess("Backup ceremony complete: %s (copy it OFF this host before continuing)", backupOutput)
	printEmptyLine()

	// 4. Confirm and submit.
	if !assumeYes && !promptForConfirmation("Submit the rotation transaction now?") {
		printNote("Rotation aborted — nothing was submitted; the backup bundle can be deleted.")
		return nil
	}

	// Resolve the at-rest passphrase up front and stage the new key file, so
	// the post-submit cutover is two renames that cannot prompt or fail on
	// encryption.
	atRestPath, err := custody.ResolvePassphrasePath(effective.EncryptionKeyPassphrase, effective.EncryptionPrivateKeyPath)
	if err != nil {
		return err
	}
	atRestPassphrase, err := custody.ReadPassphraseFile(atRestPath)
	if err != nil {
		return err
	}
	stagedPath := effective.EncryptionPrivateKeyPath + ".next"
	if err := custody.SaveEncryptedShareKey(stagedPath, newPair.PrivateKey, atRestPassphrase); err != nil {
		return err
	}

	logger, err := initLogger(effective.LogLevel, effective.LogFormat)
	if err != nil {
		os.Remove(stagedPath)
		return errors.Wrap(err, "failed to initialize logger")
	}
	defer func() { _ = logger.Sync() }()
	client, err := blockchain.NewClient(effective, logger)
	if err != nil {
		os.Remove(stagedPath)
		return errors.Wrap(err, "failed to create chain client")
	}
	defer func() { _ = client.Close() }()

	submitCtx, cancel := context.WithTimeout(context.Background(), effective.RequestTimeout)
	txHash, err := client.GuardianRotateKey(submitCtx, newPair.PublicKey[:])
	cancel()
	if err != nil {
		os.Remove(stagedPath)
		return errors.Wrap(err, "rotation transaction failed — local keys are unchanged")
	}

	// The broadcast returns at CheckTx (SYNC); the message can still fail in
	// DeliverTx (interval not met, insufficient fee funds). NEVER cut the
	// local key files over until the on-chain record has actually advanced.
	printNote("Broadcast %s — waiting for on-chain confirmation…", txHash)
	confirmed := false
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), effective.RequestTimeout)
		rec, gerr := client.GetGuardian(pollCtx, effective.GuardianAddress)
		pollCancel()
		if gerr == nil && rec.CurrentKeyEpoch == currentEpoch+1 && bytes.Equal(rec.EncryptionPublicKey, newPair.PublicKey[:]) {
			confirmed = true
			break
		}
		time.Sleep(time.Second)
	}
	if !confirmed {
		os.Remove(stagedPath)
		return errors.Errorf(
			"rotation tx %s broadcast but the guardian record did not advance to epoch %d — the transaction "+
				"may have failed in DeliverTx (check it with 'timeflared query tx %s'); local keys are unchanged",
			txHash, currentEpoch+1, txHash)
	}

	// 5. Cutover: retire the old key beside the new one, promote the staged
	// key, refresh the public key file and configuration.
	retiredPath := custody.EpochKeyPath(effective.EncryptionPrivateKeyPath, currentEpoch)
	if err := os.Rename(effective.EncryptionPrivateKeyPath, retiredPath); err != nil {
		return errors.Wrapf(err,
			"rotation SUBMITTED (tx %s) but retiring the old key file failed — resolve manually: move %s to %s and %s to %s",
			txHash, effective.EncryptionPrivateKeyPath, retiredPath, stagedPath, effective.EncryptionPrivateKeyPath)
	}
	if err := os.Rename(stagedPath, effective.EncryptionPrivateKeyPath); err != nil {
		return errors.Wrapf(err,
			"rotation SUBMITTED (tx %s) but promoting the new key file failed — resolve manually: move %s to %s",
			txHash, stagedPath, effective.EncryptionPrivateKeyPath)
	}
	// The informational hex copy lives beside the private key (the layout
	// config init generates) — GetPublicKeyPath resolves into the keyring
	// directory, which is a different place.
	publicKeyPath := filepath.Join(filepath.Dir(effective.EncryptionPrivateKeyPath), config.DefaultPublicKeyFileName)
	if err := os.WriteFile(publicKeyPath, []byte(newPubHex), 0644); err != nil { //nolint:gosec // public key
		printWarning("Failed to refresh %s: %v", publicKeyPath, err)
	}
	if err := cfgManager.SetWithoutValidation("encryption_public_key", newPubHex); err != nil {
		return err
	}
	if err := cfgManager.Save(); err != nil {
		return errors.Wrap(err, "failed to save config")
	}

	printEmptyLine()
	printSeparator("Key Rotated")
	printEmptyLine()
	printf(indent1+"🧾 Transaction:   %s\n", txHash)
	printf(indent1+"🆕 New epoch:     %d — in force for selections from the NEXT block\n", currentEpoch+1)
	printf(indent1+"🔑 New key:       %s\n", newPubHex)
	printf(indent1+"🔁 Retired epoch: %d kept at %s\n", currentEpoch, retiredPath)
	printEmptyLine()
	printNote("Next steps:")
	printTextLn(indent1 + "• Restart the daemon if it is running (it also self-detects the new epoch)")
	printTextLn(indent1 + "• Keep the retired key until its LAST in-flight assignment settles — deleting")
	printTextLn(indent1 + "  it earlier means a no-reveal slash on every assignment encrypted to it")
	printTextLn(indent1 + "• Store the rotation bundle and its passphrase separately, off this host")
	printEmptyLine()

	return nil
}
