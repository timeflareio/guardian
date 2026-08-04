package chain

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timeflareio/guardian/internal/config"
)

// TestPassphraseReaderReplays: the file backend prompts on every open over
// the daemon's lifetime — the reader must serve the passphrase line forever,
// including through bufio line reads.
func TestPassphraseReaderReplays(t *testing.T) {
	r := bufio.NewReader(&passphraseReader{line: []byte("secret-pass\n")})

	for i := 0; i < 10; i++ {
		line, err := r.ReadString('\n')
		require.NoError(t, err, "read %d", i)
		assert.Equal(t, "secret-pass\n", line, "read %d", i)
	}
}

// TestSuppressKeyringTTYPrompts pins the fix for interactive terminals: once
// a passphrase is configured, os.Stdin must not be a TTY, because the sdk's
// prompt path (client/input.GetPassword) decides interactivity from os.Stdin
// and would read the passphrase from the terminal instead of our reader.
func TestSuppressKeyringTTYPrompts(t *testing.T) {
	suppressKeyringTTYPrompts()
	first := os.Stdin

	info, err := first.Stat()
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeNamedPipe, "stdin must be a pipe (never a TTY) after suppression")

	// Idempotent: a second call must not re-swap.
	suppressKeyringTTYPrompts()
	assert.Same(t, first, os.Stdin)
}

// testPassphraseFile writes a raw passphrase file the way the devnet and
// `config init` do: the file content IS the passphrase, never encoded.
func testPassphraseFile(t *testing.T, dir, passphrase string) string {
	t.Helper()
	path := filepath.Join(dir, "keyring_passphrase")
	require.NoError(t, os.WriteFile(path, []byte(passphrase), 0o600))
	return path
}

// TestNewKeyringWithPassphraseFile exercises the full automation contract:
// a file-backend keyring created and re-opened purely from the configured
// passphrase file — key creation, unlock, and address resolution, with no
// interactive input available (t.Setenv-free, stdin is the suppression pipe).
func TestNewKeyringWithPassphraseFile(t *testing.T) {
	dir := t.TempDir()
	passphrase := "test-automation-passphrase"

	cfg := config.DefaultConfig()
	cfg.KeyringBackend = "file"
	cfg.KeyringDir = dir
	cfg.KeyName = "test-key"
	cfg.KeyringPassphrase = testPassphraseFile(t, dir, passphrase)

	kr, err := NewKeyring(cfg)
	require.NoError(t, err)

	// Creating the key sets the keyring's passphrase from our reader (the
	// file backend prompts for it twice on first use).
	record, _, err := kr.NewMnemonic("test-key", keyring.English, sdk.FullFundraiserPath, keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	require.NoError(t, err)
	created, err := record.GetAddress()
	require.NoError(t, err)

	// A fresh keyring instance must unlock from the passphrase file alone.
	resolved, err := ResolveKeyAddress(cfg, "test-key")
	require.NoError(t, err)
	assert.Equal(t, created.String(), resolved)
	assert.NoError(t, config.ValidateGuardianAddress(resolved), "resolved address carries the chain prefix")

	// The in-memory variant (config init path) must agree.
	direct, err := ResolveKeyAddressWithPassphrase("file", dir, "test-key", passphrase)
	require.NoError(t, err)
	assert.Equal(t, resolved, direct)
}

// TestNewKeyringWrongPassphraseFails: a wrong configured passphrase must fail
// loudly, not hang waiting for input.
func TestNewKeyringWrongPassphraseFails(t *testing.T) {
	dir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.KeyringBackend = "file"
	cfg.KeyringDir = dir
	cfg.KeyName = "test-key"
	cfg.KeyringPassphrase = testPassphraseFile(t, dir, "right-passphrase")

	kr, err := NewKeyring(cfg)
	require.NoError(t, err)
	_, _, err = kr.NewMnemonic("test-key", keyring.English, sdk.FullFundraiserPath, keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	require.NoError(t, err)

	_, err = ResolveKeyAddressWithPassphrase("file", dir, "test-key", "wrong-passphrase")
	require.Error(t, err)
}
