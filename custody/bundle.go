package custody

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	crypto "github.com/timeflareio/crypto/go"
)

// BundleVersion is the current backup bundle schema version. Version 2 added
// the retired key epochs (key rotation); version-1 bundles (single key, no
// epochs) still open and restore.
const BundleVersion = 2

// Bundle is everything a guardian needs to come back from bare metal: the
// share-encryption private key, the signing keyring files, and enough
// identity context to verify the restore against the chain. It only ever
// exists encrypted on disk (SealBundle/OpenBundle) — the JSON form is an
// in-memory intermediate.
type Bundle struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`

	// Identity context — lets restore verify against the registered guardian
	// record before declaring success.
	ChainID             string `json:"chain_id"`
	KeyName             string `json:"key_name"`
	GuardianAddress     string `json:"guardian_address"`
	EncryptionPublicKey string `json:"encryption_public_key"` // 64 hex chars

	// Key material. SharePrivateKey is the CURRENT epoch's key; RetiredKeys
	// carries every retired epoch still held locally (epoch → raw 32 bytes) —
	// the whole epoch keyring travels in one bundle, so a restore can serve
	// in-flight assignments encrypted to any epoch. Absent in version-1
	// bundles.
	SharePrivateKey []byte            `json:"share_private_key"`      // raw 32 bytes
	RetiredKeys     map[uint64][]byte `json:"retired_keys,omitempty"` // epoch → raw 32 bytes
	KeyringFiles    map[string][]byte `json:"keyring_files"`          // path relative to keyring_dir → content

	// CurrentKeyEpoch records which epoch SharePrivateKey occupies (0 for
	// version-1 bundles and never-rotated guardians).
	CurrentKeyEpoch uint64 `json:"current_key_epoch,omitempty"`

	// ConfigFingerprint is the SHA256 of config.yaml at backup time —
	// informational, for detecting config drift at restore.
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
}

// Validate checks internal consistency: schema version, key lengths, and that
// the private key actually derives the recorded public key.
func (b *Bundle) Validate() error {
	if b.Version < 1 || b.Version > BundleVersion {
		return fmt.Errorf("unsupported bundle version %d (this build understands versions 1-%d)", b.Version, BundleVersion)
	}
	if len(b.SharePrivateKey) != shareKeySize {
		return fmt.Errorf("bundle share key is %d bytes, expected %d", len(b.SharePrivateKey), shareKeySize)
	}
	for epoch, retired := range b.RetiredKeys {
		if len(retired) != shareKeySize {
			return fmt.Errorf("bundle retired key for epoch %d is %d bytes, expected %d", epoch, len(retired), shareKeySize)
		}
		if epoch >= b.CurrentKeyEpoch {
			return fmt.Errorf("bundle retired key epoch %d is not below the current epoch %d", epoch, b.CurrentKeyEpoch)
		}
	}

	var key [32]byte
	copy(key[:], b.SharePrivateKey)
	defer Zero(key[:])
	derived, err := crypto.DerivePublicKey(key)
	if err != nil {
		return fmt.Errorf("failed to derive public key from bundle share key: %w", err)
	}
	if hex.EncodeToString(derived[:]) != b.EncryptionPublicKey {
		return fmt.Errorf("bundle share key does not derive the recorded public key — the bundle is corrupt")
	}
	return nil
}

// CollectRetiredKeys loads every retired epoch key file found beside the
// current key (<keyPath>.epoch<N> → raw key), using the same passphrase for
// each. Epoch gaps are normal — a retired key legitimately disappears once
// its last assignment settles and the operator deletes it.
func CollectRetiredKeys(keyPath string, passphrase func() (string, error)) (map[uint64][]byte, error) {
	matches, err := filepath.Glob(keyPath + ".epoch*")
	if err != nil {
		return nil, fmt.Errorf("failed to scan for retired key files: %w", err)
	}

	retired := map[uint64][]byte{}
	for _, path := range matches {
		suffix := strings.TrimPrefix(path, keyPath+".epoch")
		epoch, err := strconv.ParseUint(suffix, 10, 64)
		if err != nil {
			continue // not an epoch key file (e.g. an operator's .epoch0.bak copy)
		}
		key, err := LoadShareKey(path, passphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to load retired key epoch %d: %w", epoch, err)
		}
		retired[epoch] = append([]byte(nil), key[:]...)
		Zero(key[:])
	}
	return retired, nil
}

// RestoreRetiredKeys writes bundled retired keys to their conventional epoch
// paths as encrypted envelopes under the given passphrase.
func RestoreRetiredKeys(keyPath string, retired map[uint64][]byte, passphrase string) error {
	for epoch, raw := range retired {
		if len(raw) != shareKeySize {
			return fmt.Errorf("retired key epoch %d is %d bytes, expected %d", epoch, len(raw), shareKeySize)
		}
		var key [32]byte
		copy(key[:], raw)
		err := SaveEncryptedShareKey(EpochKeyPath(keyPath, epoch), key, passphrase)
		Zero(key[:])
		if err != nil {
			return fmt.Errorf("failed to restore retired key epoch %d: %w", epoch, err)
		}
	}
	return nil
}

// SealBundle validates and encrypts the bundle under a backup passphrase.
func SealBundle(b *Bundle, passphrase string) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("failed to encode backup bundle: %w", err)
	}
	defer Zero(payload)
	return Seal(payload, passphrase)
}

// OpenBundle decrypts and validates a bundle produced by SealBundle.
func OpenBundle(blob []byte, passphrase string) (*Bundle, error) {
	payload, err := Open(blob, passphrase)
	if err != nil {
		return nil, err
	}
	defer Zero(payload)

	var b Bundle
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("failed to decode backup bundle: %w", err)
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

// CollectKeyringFiles reads every regular file under keyringDir into a
// relative-path → content map for bundling. A missing directory yields an
// empty map (the share key alone is still a valid backup).
func CollectKeyringFiles(keyringDir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if _, err := os.Stat(keyringDir); os.IsNotExist(err) {
		return files, nil
	}

	err := filepath.WalkDir(keyringDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(keyringDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read keyring file %s: %w", path, err)
		}
		files[rel] = content
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to collect keyring files from %s: %w", keyringDir, err)
	}
	return files, nil
}

// RestoreKeyringFiles writes bundled keyring files back under keyringDir
// (0700 directories, 0600 files). It refuses to overwrite existing files
// unless force is set — a restore must never silently clobber a live
// keyring.
func RestoreKeyringFiles(keyringDir string, files map[string][]byte, force bool) error {
	for rel := range files {
		target := filepath.Join(keyringDir, rel)
		if _, err := os.Stat(target); err == nil && !force {
			return fmt.Errorf("keyring file %s already exists — re-run with --force to overwrite", target)
		}
	}
	for rel, content := range files {
		target := filepath.Join(keyringDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return fmt.Errorf("failed to create keyring directory for %s: %w", target, err)
		}
		if err := os.WriteFile(target, content, 0600); err != nil {
			return fmt.Errorf("failed to restore keyring file %s: %w", target, err)
		}
	}
	return nil
}
