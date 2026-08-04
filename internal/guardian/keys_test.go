package guardian

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/custody"
	"github.com/timeflareio/guardian/internal/guardian/mocks"
)

// Epoch key resolution: derive the epoch in force from a secret's creation
// height against the on-chain history, load the matching local key (current
// at the configured path, retired beside it), and fall back to trial-decrypt.

// setupRotatedGuardian builds a guardian that rotated once: epoch 0's key is
// retired on disk (<key>.epoch0), epoch 1's key is current, and the mock
// chain carries the matching history (epoch 0 effective from 0, epoch 1 from
// rotationHeight).
func setupRotatedGuardian(t *testing.T, mockChain *mocks.MockChain, rotationHeight int64) (cfg *config.Config, oldPair, newPair *crypto.KeyPair) {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private_key")

	var err error
	oldPair, err = crypto.GenerateKeypair()
	require.NoError(t, err)
	newPair, err = crypto.GenerateKeypair()
	require.NoError(t, err)

	require.NoError(t, custody.WritePassphraseFile(custody.SiblingPassphrasePath(keyPath), "test-pass"))
	require.NoError(t, custody.SaveEncryptedShareKey(custody.EpochKeyPath(keyPath, 0), oldPair.PrivateKey, "test-pass"))
	require.NoError(t, custody.SaveEncryptedShareKey(keyPath, newPair.PrivateKey, "test-pass"))

	cfg = mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = keyPath

	mockChain.SetKeyHistory(cfg.GuardianAddress, []chain.KeyEpoch{
		{Epoch: 0, PublicKey: oldPair.PublicKey[:], EffectiveFromHeight: 0},
		{Epoch: 1, PublicKey: newPair.PublicKey[:], EffectiveFromHeight: rotationHeight},
	})

	return cfg, oldPair, newPair
}

func TestEpochKeyResolver_DerivationAndLoading(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	cfg, oldPair, newPair := setupRotatedGuardian(t, mockChain, 100)
	client := mocks.NewMockClient(mockChain, logger)
	resolver := NewEpochKeyResolver(cfg, client, logger)
	ctx := context.Background()

	// Heights below the rotation resolve epoch 0 (the retired key)…
	epoch, err := resolver.EpochAt(ctx, 99)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), epoch)
	key, epoch, err := resolver.KeyForHeight(ctx, 50)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), epoch)
	assert.Equal(t, oldPair.PrivateKey, key)

	// …the rotation's effective height and beyond resolve epoch 1 (current)
	key, epoch, err = resolver.KeyForHeight(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), epoch)
	assert.Equal(t, newPair.PrivateKey, key)

	// The trial fallback offers both keys, newest epoch first
	trial := resolver.TrialKeys(ctx)
	require.Len(t, trial, 2)
	assert.Equal(t, uint64(1), trial[0].Epoch)
	assert.Equal(t, uint64(0), trial[1].Epoch)

	// Nothing is missing for the epochs in use
	assert.Empty(t, resolver.MissingEpochKeys(ctx, map[uint64]bool{0: true, 1: true}))
}

func TestEpochKeyResolver_EmptyHistoryIsSingleEpoch(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	client := mocks.NewMockClient(mockChain, logger)
	cfg := mocks.CreateTestConfig()
	resolver := NewEpochKeyResolver(cfg, client, logger)

	// Pre-rotation chain state (no history): everything derives to epoch 0
	epoch, err := resolver.EpochAt(context.Background(), 12345)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), epoch)
}

func TestEpochKeyResolver_MissingRetiredKeyReported(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	cfg, _, _ := setupRotatedGuardian(t, mockChain, 100)
	client := mocks.NewMockClient(mockChain, logger)
	resolver := NewEpochKeyResolver(cfg, client, logger)
	ctx := context.Background()

	// Delete the retired epoch file — epoch 0 becomes unavailable
	require.NoError(t, os.Remove(custody.EpochKeyPath(cfg.EncryptionPrivateKeyPath, 0)))
	cfg.WipeEncryptionKey()

	missing := resolver.MissingEpochKeys(ctx, map[uint64]bool{0: true, 1: true})
	assert.Equal(t, []uint64{0}, missing)
}

// TestDecryptShare_ResolvesRetiredEpoch drives the real decrypt path: a
// secret created BEFORE the rotation (encrypted to the retired epoch-0 key)
// decrypts automatically, and one created after uses the current key.
func TestDecryptShare_ResolvesRetiredEpoch(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	cfg, oldPair, newPair := setupRotatedGuardian(t, mockChain, 100)
	client := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, client, logger)
	ctx := context.Background()

	payload := []byte("share encrypted before the rotation")
	oldCiphertext, err := mocks.CreateProperlyEncryptedShareForTesting(payload, oldPair.PublicKey)
	require.NoError(t, err)

	preRotation := &chain.Secret{ID: "pre-rotation-secret", CreatedAt: 50}
	out, err := service.decryptShare(ctx, preRotation, oldCiphertext)
	require.NoError(t, err, "an assignment from before the rotation must decrypt with the retired epoch key")
	assert.Equal(t, payload, out)

	newPayload := []byte("share encrypted after the rotation")
	newCiphertext, err := mocks.CreateProperlyEncryptedShareForTesting(newPayload, newPair.PublicKey)
	require.NoError(t, err)

	postRotation := &chain.Secret{ID: "post-rotation-secret", CreatedAt: 150}
	out, err = service.decryptShare(ctx, postRotation, newCiphertext)
	require.NoError(t, err)
	assert.Equal(t, newPayload, out)
}

// TestDecryptShare_TrialFallbackCoversStaleDerivation pins the belt and
// braces: even when the secret's creation height derives the WRONG epoch
// (e.g. a zero height from stale data), the keyring trial pass still
// decrypts.
func TestDecryptShare_TrialFallbackCoversStaleDerivation(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	cfg, _, newPair := setupRotatedGuardian(t, mockChain, 100)
	client := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, client, logger)

	payload := []byte("encrypted to the CURRENT key")
	ciphertext, err := mocks.CreateProperlyEncryptedShareForTesting(payload, newPair.PublicKey)
	require.NoError(t, err)

	// CreatedAt 0 derives epoch 0 — the wrong key for this ciphertext
	stale := &chain.Secret{ID: "stale-height-secret", CreatedAt: 0}
	out, err := service.decryptShare(context.Background(), stale, ciphertext)
	require.NoError(t, err, "the trial-decrypt fallback must recover from a wrong derivation")
	assert.Equal(t, payload, out)
}
