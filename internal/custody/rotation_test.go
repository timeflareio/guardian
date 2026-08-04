package custody

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crypto "github.com/timeflareio/crypto/go"
)

// Key-rotation custody coverage: the epoch key file layout beside the current
// key, bundle v2 carrying the whole keyring, and v1 bundles still opening.

func TestEpochKeyPathLayout(t *testing.T) {
	assert.Equal(t, "/g/private_key.epoch0", EpochKeyPath("/g/private_key", 0))
	assert.Equal(t, "/g/private_key.epoch12", EpochKeyPath("/g/private_key", 12))
}

func TestCollectAndRestoreRetiredKeys(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private_key")
	pass := func() (string, error) { return "at-rest-pass", nil }

	// Two retired epochs on disk (0 and 2 — epoch 1 already deleted after
	// settlement; gaps are normal), plus a decoy that is not an epoch file.
	k0, k2 := testKey(t), testKey(t)
	require.NoError(t, SaveEncryptedShareKey(EpochKeyPath(keyPath, 0), k0, "at-rest-pass"))
	require.NoError(t, SaveEncryptedShareKey(EpochKeyPath(keyPath, 2), k2, "at-rest-pass"))
	require.NoError(t, os.WriteFile(keyPath+".epoch0.bak", []byte("not a key"), 0600))

	retired, err := CollectRetiredKeys(keyPath, pass)
	require.NoError(t, err)
	require.Len(t, retired, 2)
	assert.Equal(t, k0[:], retired[0])
	assert.Equal(t, k2[:], retired[2])

	// Restore them into a fresh layout under a different passphrase
	dst := t.TempDir()
	dstKeyPath := filepath.Join(dst, "private_key")
	require.NoError(t, RestoreRetiredKeys(dstKeyPath, retired, "new-pass"))
	restored, err := LoadShareKey(EpochKeyPath(dstKeyPath, 2), func() (string, error) { return "new-pass", nil })
	require.NoError(t, err)
	assert.Equal(t, k2, restored)
}

func TestBundleV2CarriesRetiredKeys(t *testing.T) {
	bundle, key := makeTestBundle(t)
	retired := testKey(t)
	bundle.RetiredKeys = map[uint64][]byte{0: retired[:]}
	bundle.CurrentKeyEpoch = 1

	blob, err := SealBundle(bundle, "backup-pass")
	require.NoError(t, err)
	out, err := OpenBundle(blob, "backup-pass")
	require.NoError(t, err)
	assert.Equal(t, key[:], out.SharePrivateKey)
	assert.Equal(t, retired[:], out.RetiredKeys[0])
	assert.Equal(t, uint64(1), out.CurrentKeyEpoch)
}

func TestBundleV2ValidatesRetiredKeys(t *testing.T) {
	bundle, _ := makeTestBundle(t)
	bundle.CurrentKeyEpoch = 1
	bundle.RetiredKeys = map[uint64][]byte{0: []byte("short")}
	require.Error(t, bundle.Validate(), "short retired key rejected")

	bundle, _ = makeTestBundle(t)
	retired := testKey(t)
	bundle.CurrentKeyEpoch = 1
	bundle.RetiredKeys = map[uint64][]byte{1: retired[:]} // not below current
	err := bundle.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not below the current epoch")
}

// TestBundleV1StillOpens pins backwards compatibility: a version-1 bundle
// (single key, no epochs) sealed by an older build restores unchanged.
func TestBundleV1StillOpens(t *testing.T) {
	bundle, key := makeTestBundle(t)
	bundle.Version = 1
	bundle.RetiredKeys = nil
	bundle.CurrentKeyEpoch = 0

	blob, err := SealBundle(bundle, "backup-pass")
	require.NoError(t, err)
	out, err := OpenBundle(blob, "backup-pass")
	require.NoError(t, err)
	assert.Equal(t, 1, out.Version)
	assert.Equal(t, key[:], out.SharePrivateKey)
	assert.Empty(t, out.RetiredKeys)

	pub, err := crypto.DerivePublicKey(key)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(pub[:]), out.EncryptionPublicKey)
}
