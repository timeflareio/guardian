package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/custody"
)

// migrate-key encrypts a legacy plaintext key in place. In place means there is
// no second copy to fall back on, so the only acceptable outcomes are "the
// envelope decrypts to exactly the original key" and "nothing changed".
func TestConfigMigrateKeyEncryptsInPlace(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")
	keyPath := g.get("encryption-private-key-path")

	// Replace the generated envelope with a legacy raw 32-byte key.
	original, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, original.PrivateKey[:], 0600); err != nil {
		t.Fatal(err)
	}
	if encrypted, err := custody.IsEncryptedKeyFile(keyPath); err != nil || encrypted {
		t.Fatalf("precondition: expected a plaintext key file (encrypted=%v, err=%v)", encrypted, err)
	}

	passFile := filepath.Join(g.offhost, "kek")
	if err := custody.WritePassphraseFile(passFile, "new-at-rest"); err != nil {
		t.Fatal(err)
	}

	g.mustRun("", "config", "migrate-key", "--passphrase-file", passFile, "--accept")

	encrypted, err := custody.IsEncryptedKeyFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !encrypted {
		t.Fatal("migrate-key left the key in plaintext")
	}
	reloaded, err := custody.LoadShareKey(keyPath, func() (string, error) { return "new-at-rest", nil })
	if err != nil {
		t.Fatalf("migrated key does not decrypt: %v", err)
	}
	if reloaded != original.PrivateKey {
		t.Fatal("the migrated envelope decrypts to a different key — every share encrypted to the original is now unreadable")
	}
	// The passphrase file has to be recorded, or the daemon cannot decrypt
	// unattended after the migration.
	if got := g.get("encryption-key-passphrase"); got != passFile {
		t.Errorf("encryption_key_passphrase is %q, want %q", got, passFile)
	}
}

// Running it twice must be a no-op rather than an envelope inside an envelope.
func TestConfigMigrateKeyIsIdempotent(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")
	keyPath := g.get("encryption-private-key-path")

	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	out := g.mustRun("", "config", "migrate-key", "--accept")
	if !strings.Contains(out, "already encrypted") {
		t.Errorf("expected an already-encrypted notice, got:\n%s", out)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("migrate-key rewrote a key that was already encrypted")
	}
}

// create-encryption-key must never overwrite: the key it would replace is the
// one shares are encrypted to.
//
// Note the naming: this command writes <file-name>.key and <file-name>.hex,
// whereas config init writes private_key and public_key.hex. The two schemes do
// not meet, so pointing this command at init's layout does not collide with it —
// which is why the collision below is built from two runs of this command.
func TestCreateEncryptionKeyRefusesToOverwrite(t *testing.T) {
	g := newFixture(t)
	dir := filepath.Join(g.dir, "keys")
	passFile := filepath.Join(g.offhost, "kek")
	if err := custody.WritePassphraseFile(passFile, "secret"); err != nil {
		t.Fatal(err)
	}

	g.mustRun("", "config", "create-encryption-key",
		"--directory", dir, "--file-name", "share", "--passphrase-file", passFile)

	keyPath := filepath.Join(dir, "share.key")
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = g.run("", "config", "create-encryption-key",
		"--directory", dir, "--file-name", "share", "--passphrase-file", passFile)
	if err == nil {
		t.Fatal("create-encryption-key overwrote an existing key")
	}
	if !strings.Contains(err.Error(), "already exist") {
		t.Errorf("error does not explain the refusal: %v", err)
	}

	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the existing key was modified")
	}
}

// The generated keypair must be internally consistent: the hex file is what an
// operator registers on-chain, and the private key is what decrypts shares sent
// to it. A mismatch means every share is undecryptable.
func TestCreateEncryptionKeyProducesMatchingPair(t *testing.T) {
	g := newFixture(t)
	dir := filepath.Join(g.dir, "keys")
	passFile := filepath.Join(g.offhost, "kek")
	if err := custody.WritePassphraseFile(passFile, "secret"); err != nil {
		t.Fatal(err)
	}

	g.mustRun("", "config", "create-encryption-key",
		"--directory", dir, "--file-name", "share", "--passphrase-file", passFile)

	priv, err := custody.LoadShareKey(filepath.Join(dir, "share.key"),
		func() (string, error) { return "secret", nil })
	if err != nil {
		t.Fatalf("generated key does not load: %v", err)
	}
	pubHex, err := os.ReadFile(filepath.Join(dir, "share.hex"))
	if err != nil {
		t.Fatal(err)
	}
	derived, err := crypto.DerivePublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := crypto.BytesToHex(derived[:])
	if err != nil {
		t.Fatal(err)
	}
	if string(pubHex) != expected {
		t.Errorf("share.hex is %s but the private key derives %s", pubHex, expected)
	}
	if mode := g.mode(filepath.Join(dir, "share.key")); mode != 0600 {
		t.Errorf("private key mode %04o, want 0600", mode)
	}
}
