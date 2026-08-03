package custody

import (
	"fmt"

	bip39 "github.com/cosmos/go-bip39"
)

// Recovery mnemonic per docs/guides/CLIENT_CONVENTIONS.md §5: the 24 words
// encode the raw 32-byte X25519 private scalar directly as BIP39 entropy —
// one mnemonic per key, self-contained, no derivation scheme. The encrypted
// bundle (bundle.go) is the primary backup artefact; the mnemonic is the
// human-typable fallback.

// KeyToMnemonic encodes the raw 32-byte share key as a 24-word BIP39
// mnemonic (English wordlist).
func KeyToMnemonic(key [32]byte) (string, error) {
	mnemonic, err := bip39.NewMnemonic(key[:])
	if err != nil {
		return "", fmt.Errorf("failed to encode recovery mnemonic: %w", err)
	}
	return mnemonic, nil
}

// KeyFromMnemonic decodes a 24-word BIP39 mnemonic back to the raw 32-byte
// share key — the exact bytes KeyToMnemonic was given (the mnemonic
// round-trips the stored key byte-exactly).
func KeyFromMnemonic(mnemonic string) ([32]byte, error) {
	var key [32]byte
	// MnemonicToByteArray validates the checksum and returns the entropy
	// with the checksum byte appended (33 bytes for a 24-word mnemonic).
	raw, err := bip39.MnemonicToByteArray(mnemonic)
	if err != nil {
		return key, fmt.Errorf("invalid recovery mnemonic: %w", err)
	}
	defer Zero(raw)
	if len(raw) != shareKeySize+1 {
		return key, fmt.Errorf("recovery mnemonic encodes %d bytes, expected %d (a share-key mnemonic is always 24 words)", len(raw)-1, shareKeySize)
	}
	copy(key[:], raw[:shareKeySize])
	return key, nil
}
