package custody

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	crypto "github.com/timeflareio/crypto/go"
)

func testKey(t *testing.T) [32]byte {
	t.Helper()
	keypair, err := crypto.GenerateKeypair()
	require.NoError(t, err)
	return keypair.PrivateKey
}

// ── Envelope ────────────────────────────────────────────────────────────────

func TestEnvelopeRoundTrip(t *testing.T) {
	payload := []byte("the quick brown fox")
	blob, err := Seal(payload, "correct horse")
	require.NoError(t, err)

	assert.True(t, IsEnvelope(blob))

	out, err := Open(blob, "correct horse")
	require.NoError(t, err)
	assert.Equal(t, payload, out)
}

func TestEnvelopeWrongPassphrase(t *testing.T) {
	blob, err := Seal([]byte("secret"), "right")
	require.NoError(t, err)

	_, err = Open(blob, "wrong")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong passphrase or corrupted")
}

func TestEnvelopeTamperDetected(t *testing.T) {
	blob, err := Seal([]byte("secret"), "pass")
	require.NoError(t, err)

	// Flip a bit in the ciphertext…
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0x01
	_, err = Open(tampered, "pass")
	assert.Error(t, err, "ciphertext tampering must be detected")

	// …in the header (KDF parameters are AEAD additional data; a changed
	// threads value stays within the sanity bounds but derives a different
	// key, so authentication fails)…
	tampered = append([]byte(nil), blob...)
	tampered[13] ^= 0x01 // argon2 threads parameter
	_, err = Open(tampered, "pass")
	assert.Error(t, err, "header tampering must be detected")

	// …and out-of-bounds KDF parameters are rejected BEFORE the KDF runs —
	// a flipped bit in the time parameter must not buy a near-infinite
	// argon2 (DoS via corrupt file).
	tampered = append([]byte(nil), blob...)
	tampered[6] ^= 0x01 // argon2 time parameter → 65539 passes
	start := time.Now()
	_, err = Open(tampered, "pass")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of bounds")
	assert.Less(t, time.Since(start), 2*time.Second, "bounds check must reject without running the KDF")
}

func TestEnvelopeRejectsBadInput(t *testing.T) {
	_, err := Seal(nil, "pass")
	assert.Error(t, err, "empty payload")

	_, err = Seal([]byte("x"), "")
	assert.Error(t, err, "empty passphrase")

	_, err = Open([]byte("not an envelope"), "pass")
	assert.Error(t, err)

	_, err = Open(append(append([]byte(nil), envelopeMagic...), 0x02), "pass")
	assert.Error(t, err, "unknown version must be rejected")
}

// ── Key file ────────────────────────────────────────────────────────────────

func TestLoadShareKeyLegacyPlaintext(t *testing.T) {
	key := testKey(t)
	path := filepath.Join(t.TempDir(), "private_key")
	require.NoError(t, os.WriteFile(path, key[:], 0600))

	loaded, err := LoadShareKey(path, func() (string, error) {
		t.Fatal("plaintext load must not request a passphrase")
		return "", nil
	})
	require.NoError(t, err)
	assert.Equal(t, key, loaded)
}

func TestSaveAndLoadEncryptedShareKey(t *testing.T) {
	key := testKey(t)
	path := filepath.Join(t.TempDir(), "private_key")

	require.NoError(t, SaveEncryptedShareKey(path, key, "kek-pass"))

	encrypted, err := IsEncryptedKeyFile(path)
	require.NoError(t, err)
	assert.True(t, encrypted)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	loaded, err := LoadShareKey(path, func() (string, error) { return "kek-pass", nil })
	require.NoError(t, err)
	assert.Equal(t, key, loaded)

	_, err = LoadShareKey(path, func() (string, error) { return "wrong", nil })
	assert.Error(t, err)
}

func TestLoadShareKeyGarbageRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private_key")
	require.NoError(t, os.WriteFile(path, []byte("neither format"), 0600))

	_, err := LoadShareKey(path, func() (string, error) { return "", nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither an encrypted envelope nor a raw")
}

func TestResolvePassphrasePath(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private_key")

	// Nothing exists → error naming the sibling.
	_, err := ResolvePassphrasePath("", keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), PassphraseFileName)

	// Sibling exists → used when nothing is configured.
	sibling := SiblingPassphrasePath(keyPath)
	require.NoError(t, WritePassphraseFile(sibling, "sibling-pass"))
	resolved, err := ResolvePassphrasePath("", keyPath)
	require.NoError(t, err)
	assert.Equal(t, sibling, resolved)

	// Configured path wins when it exists.
	configured := filepath.Join(dir, "elsewhere")
	require.NoError(t, WritePassphraseFile(configured, "configured-pass"))
	resolved, err = ResolvePassphrasePath(configured, keyPath)
	require.NoError(t, err)
	assert.Equal(t, configured, resolved)

	// Configured-but-missing falls back to the sibling (the container case:
	// config carries init-environment paths, env re-points the key path).
	resolved, err = ResolvePassphrasePath(filepath.Join(dir, "missing"), keyPath)
	require.NoError(t, err)
	assert.Equal(t, sibling, resolved)
}

func TestPassphraseFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pass")
	require.NoError(t, WritePassphraseFile(path, "s3cret phrase"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	out, err := ReadPassphraseFile(path)
	require.NoError(t, err)
	assert.Equal(t, "s3cret phrase", out)
}

func TestReadPassphraseFilePlainText(t *testing.T) {
	// The file content IS the passphrase, whitespace-trimmed.
	path := filepath.Join(t.TempDir(), "pass")
	require.NoError(t, os.WriteFile(path, []byte("plain-secret!\n"), 0600))

	out, err := ReadPassphraseFile(path)
	require.NoError(t, err)
	assert.Equal(t, "plain-secret!", out)
}

func TestReadPassphraseFileNeverDecodes(t *testing.T) {
	// Guardian sweep finding 30: a raw passphrase that happens to parse as
	// base64 (length a multiple of four over the base64 alphabet) must be
	// returned verbatim, never silently decoded into different bytes.
	for _, passphrase := range []string{"correcthorse", "Passw0rd", "dGVzdA=="} {
		path := filepath.Join(t.TempDir(), "pass")
		require.NoError(t, os.WriteFile(path, []byte(passphrase+"\n"), 0600))

		out, err := ReadPassphraseFile(path)
		require.NoError(t, err)
		assert.Equal(t, passphrase, out, "%q must read back verbatim", passphrase)
	}
}

// ── Mnemonic (CLIENT_CONVENTIONS.md §5) ─────────────────────────────────────

func TestMnemonicRoundTrip(t *testing.T) {
	key := testKey(t)

	mnemonic, err := KeyToMnemonic(key)
	require.NoError(t, err)
	assert.Len(t, strings.Fields(mnemonic), 24, "a 32-byte key encodes as exactly 24 words")

	restored, err := KeyFromMnemonic(mnemonic)
	require.NoError(t, err)
	assert.Equal(t, key, restored, "the mnemonic round-trips the stored bytes byte-exactly")
}

func TestMnemonicInvalidRejected(t *testing.T) {
	_, err := KeyFromMnemonic("not a real mnemonic at all")
	assert.Error(t, err)

	// 12 words (16-byte entropy) is a valid BIP39 mnemonic but not a share
	// key.
	twelve := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	_, err = KeyFromMnemonic(twelve)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "24 words")
}

// ── Bundle ──────────────────────────────────────────────────────────────────

func makeTestBundle(t *testing.T) (*Bundle, [32]byte) {
	t.Helper()
	key := testKey(t)
	pub, err := crypto.DerivePublicKey(key)
	require.NoError(t, err)

	return &Bundle{
		Version:             BundleVersion,
		CreatedAt:           time.Now().UTC(),
		ChainID:             "timeflare-test",
		KeyName:             "guardian-01",
		GuardianAddress:     "tmflr1exampleaddress",
		EncryptionPublicKey: hex.EncodeToString(pub[:]),
		SharePrivateKey:     key[:],
		KeyringFiles:        map[string][]byte{"keyring-file/guardian-01.info": []byte("keydata")},
		ConfigFingerprint:   "abc123",
	}, key
}

func TestBundleRoundTrip(t *testing.T) {
	bundle, key := makeTestBundle(t)

	blob, err := SealBundle(bundle, "backup-pass")
	require.NoError(t, err)
	assert.True(t, IsEnvelope(blob))

	out, err := OpenBundle(blob, "backup-pass")
	require.NoError(t, err)
	assert.Equal(t, bundle.KeyName, out.KeyName)
	assert.Equal(t, bundle.GuardianAddress, out.GuardianAddress)
	assert.Equal(t, key[:], out.SharePrivateKey)
	assert.Equal(t, bundle.KeyringFiles, out.KeyringFiles)

	_, err = OpenBundle(blob, "wrong")
	assert.Error(t, err)
}

func TestBundleValidateCatchesCorruption(t *testing.T) {
	bundle, _ := makeTestBundle(t)

	bundle.SharePrivateKey = bundle.SharePrivateKey[:16]
	assert.Error(t, bundle.Validate(), "short key rejected")

	bundle, _ = makeTestBundle(t)
	other := testKey(t)
	bundle.SharePrivateKey = other[:]
	err := bundle.Validate()
	require.Error(t, err, "key/public-key mismatch rejected")
	assert.Contains(t, err.Error(), "does not derive the recorded public key")

	bundle, _ = makeTestBundle(t)
	bundle.Version = 99
	assert.Error(t, bundle.Validate(), "unknown version rejected")
}

func TestKeyringFilesCollectAndRestore(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "keyring-file"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(src, "keyring-file", "g.info"), []byte("info"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "keyhash"), []byte("hash"), 0600))

	files, err := CollectKeyringFiles(src)
	require.NoError(t, err)
	assert.Len(t, files, 2)
	assert.Equal(t, []byte("info"), files[filepath.Join("keyring-file", "g.info")])

	dst := t.TempDir()
	require.NoError(t, RestoreKeyringFiles(dst, files, false))
	restored, err := os.ReadFile(filepath.Join(dst, "keyring-file", "g.info"))
	require.NoError(t, err)
	assert.Equal(t, []byte("info"), restored)

	// A second restore must refuse without force…
	err = RestoreKeyringFiles(dst, files, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	// …and proceed with it.
	assert.NoError(t, RestoreKeyringFiles(dst, files, true))
}

func TestCollectKeyringFilesMissingDir(t *testing.T) {
	files, err := CollectKeyringFiles(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	assert.Empty(t, files)
}
