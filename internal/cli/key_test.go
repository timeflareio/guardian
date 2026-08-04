package cli

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/custody"
)

// A guardian's economic life depends on this round trip working. If a restore
// silently produces a different key, every reveal encrypted to the real one is
// missed and slashed — so the assertion is on the key material itself, not on
// the command reporting success.
func TestKeyBackupRestoreRoundTrip(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	keyPath := g.get("encryption-private-key-path")
	publicBefore := g.get("encryption-public-key")

	// A retired epoch key beside the current one: rotation leaves these behind
	// to serve in-flight assignments, and a backup that drops them loses every
	// reveal still bound to that epoch.
	retiredPair, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	retiredPath := custody.EpochKeyPath(keyPath, 0)
	if err := custody.SaveEncryptedShareKey(retiredPath, retiredPair.PrivateKey, "at-rest-pass"); err != nil {
		t.Fatal(err)
	}

	backupPassFile := filepath.Join(g.offhost, "backup-pass")
	if err := custody.WritePassphraseFile(backupPassFile, "backup-secret"); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(g.offhost, "guardian.tfb")

	g.mustRun("", "key", "backup", "--output", bundle, "--passphrase-file", backupPassFile)

	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("backup produced no bundle: %v", err)
	}
	if mode := g.mode(bundle); mode != 0600 {
		t.Errorf("bundle mode %04o, want 0600 — it carries the whole epoch keyring", mode)
	}

	// Destroy the local keys, as a disk loss would.
	currentBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	retiredBefore, err := os.ReadFile(retiredPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(retiredPath); err != nil {
		t.Fatal(err)
	}

	// --force is not optional here: keyring_dir is the data directory, so the
	// bundle contains config.yaml and the passphrase files, all of which still
	// exist. --offline because there is no chain in a unit test; the plan is
	// explicit that the startup self-check re-verifies against the record.
	g.mustRun("", "key", "restore", "--input", bundle,
		"--passphrase-file", backupPassFile, "--offline", "--force")

	// The restored current key must derive the same public key as before.
	restored, err := custody.LoadShareKey(keyPath,
		custody.FilePassphrase("", keyPath))
	if err != nil {
		t.Fatalf("restored key does not load: %v", err)
	}
	derived, err := crypto.DerivePublicKey(restored)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(derived[:]); got != publicBefore {
		t.Fatalf("restored key derives %s, want %s — this is a different key", got, publicBefore)
	}
	if len(currentBefore) == 0 || len(retiredBefore) == 0 {
		t.Fatal("precondition: expected non-empty key files before the restore")
	}

	// And the retired epoch key must come back too, decrypting to its original.
	restoredRetired, err := custody.LoadShareKey(retiredPath,
		custody.FilePassphrase("", keyPath))
	if err != nil {
		t.Fatalf("retired epoch key was not restored: %v", err)
	}
	if restoredRetired != retiredPair.PrivateKey {
		t.Error("restored retired epoch key differs from the original — assignments bound to that epoch would be missed")
	}
}

// The mnemonic is the human-typable fallback when no bundle is reachable, so it
// has to reconstruct the very same key.
func TestKeyBackupMnemonicReconstructsTheSameKey(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	keyPath := g.get("encryption-private-key-path")
	original, err := custody.LoadShareKey(keyPath, custody.FilePassphrase("", keyPath))
	if err != nil {
		t.Fatal(err)
	}

	passFile := filepath.Join(g.offhost, "backup-pass")
	if err := custody.WritePassphraseFile(passFile, "backup-secret"); err != nil {
		t.Fatal(err)
	}
	out := g.mustRun("", "key", "backup",
		"--output", filepath.Join(g.offhost, "b.tfb"),
		"--passphrase-file", passFile, "--show-mnemonic")

	mnemonic := extractMnemonic(t, out)
	reconstructed, err := custody.KeyFromMnemonic(mnemonic)
	if err != nil {
		t.Fatalf("the printed mnemonic does not parse: %v", err)
	}
	if reconstructed != original {
		t.Error("the printed 24 words reconstruct a different key")
	}
}

// extractMnemonic pulls the 24-word line out of the backup output.
func extractMnemonic(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if words := strings.Fields(line); len(words) == 24 {
			return strings.Join(words, " ")
		}
	}
	t.Fatalf("no 24-word phrase in output:\n%s", out)
	return ""
}

// Backing up while the configuration disagrees with the key on disk would
// produce a bundle labelled with a public key it does not contain.
func TestKeyBackupRefusesPublicKeyMismatch(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	other, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	g.mustRun("", "config", "set", "encryption-public-key", hex.EncodeToString(other.PublicKey[:]))

	passFile := filepath.Join(g.offhost, "backup-pass")
	if err := custody.WritePassphraseFile(passFile, "backup-secret"); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(g.offhost, "b.tfb")
	_, err = g.run("", "key", "backup", "--output", bundle, "--passphrase-file", passFile)
	if err == nil {
		t.Fatal("backup succeeded despite a configured public key that the key file does not derive")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error does not explain the mismatch: %v", err)
	}
	if _, statErr := os.Stat(bundle); statErr == nil {
		t.Error("a refused backup still wrote a bundle")
	}
}

// Restoring over a live key without --force would destroy the key currently
// serving assignments.
func TestKeyRestoreRefusesToOverwriteWithoutForce(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	keyPath := g.get("encryption-private-key-path")
	passFile := filepath.Join(g.offhost, "backup-pass")
	if err := custody.WritePassphraseFile(passFile, "backup-secret"); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(g.offhost, "b.tfb")
	g.mustRun("", "key", "backup", "--output", bundle, "--passphrase-file", passFile)

	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = g.run("", "key", "restore", "--input", bundle,
		"--passphrase-file", passFile, "--offline")
	if err == nil {
		t.Fatal("restore overwrote an existing key without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error does not point at --force: %v", err)
	}

	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the existing key was modified by a refused restore")
	}
}

// A wrong backup passphrase must fail cleanly, leaving the local key untouched.
func TestKeyRestoreRejectsWrongBackupPassphrase(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	keyPath := g.get("encryption-private-key-path")
	right := filepath.Join(g.offhost, "right")
	wrong := filepath.Join(g.offhost, "wrong")
	for path, secret := range map[string]string{right: "correct-horse", wrong: "not-the-passphrase"} {
		if err := custody.WritePassphraseFile(path, secret); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(g.offhost, "b.tfb")
	g.mustRun("", "key", "backup", "--output", bundle, "--passphrase-file", right)

	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.run("", "key", "restore", "--input", bundle,
		"--passphrase-file", wrong, "--offline", "--force"); err == nil {
		t.Fatal("restore accepted a wrong backup passphrase")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a failed restore modified the local key")
	}
}
