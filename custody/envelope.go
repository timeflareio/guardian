// Package custody implements the guardian key-custody mechanics: the
// versioned passphrase-encrypted envelope for keys at rest, the share-key
// file format (encrypted-by-default, legacy plaintext still readable), the
// BIP39 recovery mnemonic (CLIENT_CONVENTIONS.md §5), and the backup/restore
// bundle.
//
// Honest threat model (guardian key custody plan, Phase 1): an unattended
// daemon must be able to decrypt its own key, so a same-host attacker with
// the daemon's privileges always wins. The encryption defends the backup
// copies and at-rest theft (stolen disk, leaked snapshot, mis-scoped backup),
// which is where most real key theft happens.
package custody

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// Envelope wire format, versioned from day one:
//
//	magic(4) "TFGK" | version(1) | argon2 time(4 BE) | argon2 memory KiB(4 BE) |
//	argon2 threads(1) | salt(16) | nonce(12) | ChaCha20-Poly1305 ciphertext+tag
//
// The full header (everything before the ciphertext) is bound as AEAD
// additional data, so KDF parameters and salt cannot be tampered with
// undetected. The KEK is argon2id(passphrase, salt) with the header's
// parameters, so files written with older cost settings stay readable.
const (
	envelopeVersion1 = 0x01

	envelopeSaltSize   = 16
	envelopeHeaderSize = 4 + 1 + 4 + 4 + 1 + envelopeSaltSize + chacha20poly1305.NonceSize

	// argon2id defaults for newly written envelopes (RFC 9106 second
	// recommended option: 64 MiB, 3 passes — sized so eight devnet guardians
	// decrypting at once stay cheap while brute force does not).
	defaultArgonTime    = 3
	defaultArgonMemory  = 64 * 1024 // KiB
	defaultArgonThreads = 4

	// Upper bounds enforced on Open. The KDF necessarily runs BEFORE the
	// AEAD can authenticate the header, so a corrupt or malicious file could
	// otherwise demand an absurd argon2 cost (a single flipped bit in the
	// time parameter means tens of thousands of passes). Bounds are generous
	// against any parameters a legitimate writer would choose.
	maxArgonTime    = 64
	maxArgonMemory  = 1 << 20 // KiB (1 GiB)
	maxArgonThreads = 64
)

var envelopeMagic = []byte("TFGK")

// IsEnvelope reports whether data begins with the encrypted-envelope magic.
func IsEnvelope(data []byte) bool {
	return len(data) >= len(envelopeMagic) && bytes.Equal(data[:len(envelopeMagic)], envelopeMagic)
}

// Seal encrypts payload under a passphrase-derived key and returns the
// self-describing envelope bytes.
func Seal(payload []byte, passphrase string) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("payload to seal cannot be empty")
	}
	if passphrase == "" {
		return nil, fmt.Errorf("passphrase cannot be empty")
	}

	salt := make([]byte, envelopeSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("salt generation failed: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce generation failed: %w", err)
	}

	header := make([]byte, 0, envelopeHeaderSize)
	header = append(header, envelopeMagic...)
	header = append(header, envelopeVersion1)
	header = binary.BigEndian.AppendUint32(header, defaultArgonTime)
	header = binary.BigEndian.AppendUint32(header, defaultArgonMemory)
	header = append(header, defaultArgonThreads)
	header = append(header, salt...)
	header = append(header, nonce...)

	kek := argon2.IDKey([]byte(passphrase), salt, defaultArgonTime, defaultArgonMemory, defaultArgonThreads, chacha20poly1305.KeySize)
	defer Zero(kek)

	cipher, err := chacha20poly1305.New(kek)
	if err != nil {
		return nil, fmt.Errorf("cipher initialisation failed: %w", err)
	}

	return cipher.Seal(header, nonce, payload, header), nil
}

// Open decrypts an envelope produced by Seal, using the KDF parameters the
// header carries.
func Open(blob []byte, passphrase string) ([]byte, error) {
	if !IsEnvelope(blob) {
		return nil, fmt.Errorf("not an encrypted key envelope (missing %q magic)", envelopeMagic)
	}
	if len(blob) < envelopeHeaderSize+chacha20poly1305.Overhead {
		return nil, fmt.Errorf("encrypted envelope truncated: %d bytes", len(blob))
	}
	if version := blob[4]; version != envelopeVersion1 {
		return nil, fmt.Errorf("unsupported envelope version %d (this build understands version %d)", version, envelopeVersion1)
	}

	header := blob[:envelopeHeaderSize]
	argonTime := binary.BigEndian.Uint32(blob[5:9])
	argonMemory := binary.BigEndian.Uint32(blob[9:13])
	argonThreads := blob[13]
	salt := blob[14 : 14+envelopeSaltSize]
	nonce := blob[14+envelopeSaltSize : envelopeHeaderSize]
	ciphertext := blob[envelopeHeaderSize:]

	if argonTime == 0 || argonTime > maxArgonTime ||
		argonMemory == 0 || argonMemory > maxArgonMemory ||
		argonThreads == 0 || argonThreads > maxArgonThreads {
		return nil, fmt.Errorf("envelope KDF parameters out of bounds (time=%d, memory=%d KiB, threads=%d) — corrupt or tampered file", argonTime, argonMemory, argonThreads)
	}

	kek := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, chacha20poly1305.KeySize)
	defer Zero(kek)

	cipher, err := chacha20poly1305.New(kek)
	if err != nil {
		return nil, fmt.Errorf("cipher initialisation failed: %w", err)
	}

	payload, err := cipher.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt envelope — wrong passphrase or corrupted file")
	}
	return payload, nil
}

// Zero best-effort wipes a byte slice holding key material.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
