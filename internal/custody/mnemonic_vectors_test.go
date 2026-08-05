package custody

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLIENT_CONVENTIONS.md §5 corpus pin: "every implementation asserts them in
// CI" (principle 3). The TypeScript SDK asserts the same vectors from
// conventions.test.ts; this is the Go side, reading them out of the wire
// contract module that carries them.
type conventionsCorpus struct {
	Mnemonic []struct {
		Name          string `json:"name"`
		PrivateKeyHex string `json:"private_key_hex"`
		Words         string `json:"words"`
	} `json:"mnemonic"`
	MnemonicInvalid []struct {
		Name   string `json:"name"`
		Words  string `json:"words"`
		Reason string `json:"reason"`
	} `json:"mnemonic_invalid"`
}

func TestMnemonicVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(chainVectorsDir(t), "client_conventions.json"))
	require.NoError(t, err)
	var corpus conventionsCorpus
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Mnemonic)
	require.NotEmpty(t, corpus.MnemonicInvalid)

	for _, v := range corpus.Mnemonic {
		keyBytes, err := hex.DecodeString(v.PrivateKeyHex)
		require.NoError(t, err, v.Name)
		require.Len(t, keyBytes, 32, v.Name)
		var key [32]byte
		copy(key[:], keyBytes)

		words, err := KeyToMnemonic(key)
		require.NoError(t, err, v.Name)
		assert.Equal(t, v.Words, words,
			"%s: encoding must emit the corpus words exactly", v.Name)

		restored, err := KeyFromMnemonic(v.Words)
		require.NoError(t, err, v.Name)
		assert.Equal(t, key, restored,
			"%s: decoding must round-trip the corpus key byte-exactly", v.Name)
	}

	for _, v := range corpus.MnemonicInvalid {
		_, err := KeyFromMnemonic(v.Words)
		assert.Error(t, err, "%s must be rejected (%s)", v.Name, v.Reason)
	}
}

// chainVectorsDir locates the corpus inside the pinned x/secrets/types module.
// The files travel with the module this daemon already requires, so there is
// nothing to fetch and nothing to check against a manifest: the Go checksum
// database already covers them, and the version in go.mod is the only pin.
func chainVectorsDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/timeflareio/chain/x/secrets/types").Output()
	require.NoError(t, err, "could not locate the pinned x/secrets/types module")
	dir := strings.TrimSpace(string(out))
	require.NotEmpty(t, dir, "the pinned x/secrets/types module has no local directory")
	return filepath.Join(dir, "testdata", "vectors")
}
