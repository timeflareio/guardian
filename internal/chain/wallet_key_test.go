package chain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timeflareio/guardian/internal/config"
)

// The shared corpus pins the mnemonic → address pairing at the chain's HD
// path across the chain keyring, the TypeScript SDK and this keyring
// (spec.md "Network Configuration"; CLIENT_CONVENTIONS.md §9). Read from
// disk — test data only, no module dependency edge.
type walletDerivationCorpus struct {
	HdPath  string `json:"hd_path"`
	Vectors []struct {
		Name                  string `json:"name"`
		Mnemonic              string `json:"mnemonic"`
		Address               string `json:"address"`
		WrongAddressCosmoshub string `json:"wrong_address_cosmoshub"`
	} `json:"vectors"`
}

func testWalletConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.KeyringBackend = "test"
	cfg.KeyringDir = t.TempDir()
	return cfg
}

func TestWalletKeyDerivationVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "wallet_derivation.json"))
	require.NoError(t, err)
	var corpus walletDerivationCorpus
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Vectors)

	require.Equal(t, corpus.HdPath, WalletHDPath(),
		"corpus hd_path and ChainCoinType (x/secrets/types) have drifted")

	cfg := testWalletConfig(t)
	for _, v := range corpus.Vectors {
		got, err := ImportWalletKey(cfg, v.Name, v.Mnemonic)
		require.NoError(t, err, v.Name)
		assert.NotEqual(t, v.WrongAddressCosmoshub, got,
			"%s: derivation regressed to the Cosmos Hub default coin type 118", v.Name)
		assert.Equal(t, v.Address, got, "%s: address mismatch at %s", v.Name, corpus.HdPath)
	}
}

func TestCreateWalletKeyRoundTrip(t *testing.T) {
	cfg := testWalletConfig(t)

	address, mnemonic, err := CreateWalletKey(cfg, "roundtrip")
	require.NoError(t, err)
	require.NotEmpty(t, mnemonic)

	// The mnemonic restores the same account in a fresh keyring — the
	// portability promise the wallet HD path exists to keep.
	restored, err := ImportWalletKey(testWalletConfig(t), "restored", mnemonic)
	require.NoError(t, err)
	assert.Equal(t, address, restored)

	// Existing names are never overwritten, in either direction.
	_, _, err = CreateWalletKey(cfg, "roundtrip")
	assert.ErrorContains(t, err, "already exists")
	_, err = ImportWalletKey(cfg, "roundtrip", mnemonic)
	assert.ErrorContains(t, err, "already exists")
}
