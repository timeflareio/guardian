package blockchain

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/config"
	"github.com/timeflareio/guardian/custody"
)

var sdkConfigOnce sync.Once

// initSDKConfig sets the chain's bech32 prefixes on the global sdk config.
// Idempotent and unsealed — safe alongside any other initialiser.
func initSDKConfig() {
	sdkConfigOnce.Do(func() {
		c := sdk.GetConfig()
		c.SetBech32PrefixForAccount(config.AddressPrefix, config.AddressPrefix+"pub")
		c.SetBech32PrefixForValidator(config.AddressPrefix+"valoper", config.AddressPrefix+"valoperpub")
		c.SetBech32PrefixForConsensusNode(config.AddressPrefix+"valcons", config.AddressPrefix+"valconspub")
	})
}

// newProtoCodec builds the codec + interface registry the keyring and tx
// machinery need, with the chain's message types registered.
func newProtoCodec() (*codec.ProtoCodec, codectypes.InterfaceRegistry) {
	initSDKConfig()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	secretstypes.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry), registry
}

var (
	suppressTTYOnce sync.Once
	// suppressTTYPipe keeps the pipe's write end referenced so its finaliser
	// cannot close the read end out from under os.Stdin.
	suppressTTYPipe *os.File //nolint:unused // held for lifetime, never written
)

// suppressKeyringTTYPrompts makes the process's stdin a pipe (never a TTY)
// before the keyring opens. The sdk's prompt path decides interactivity from
// os.Stdin — client/input.GetPassword's inputIsTty() — NOT from the userInput
// reader keyring.New receives, so on a terminal it would prompt the operator
// and ignore the configured passphrase (the old CLI proxy never hit this: the
// timeflared subprocess's stdin was a pipe). With stdin non-TTY the sdk falls
// through to our replaying passphrase reader.
//
// Called ONLY when a passphrase is explicitly configured — with no passphrase,
// interactive terminal prompting is preserved. Process-global by necessity;
// safe because every interactive guardiand prompt (config init questions,
// register/start confirmations) happens before the first keyring is
// constructed. Anything reading os.Stdin after this sees an empty pipe.
func suppressKeyringTTYPrompts() {
	suppressTTYOnce.Do(func() {
		r, w, err := os.Pipe()
		if err != nil {
			return // fall back to sdk behaviour (may prompt on a TTY)
		}
		suppressTTYPipe = w
		os.Stdin = r
	})
}

// passphraseReader replays the keyring passphrase forever: the file backend
// prompts on every open operation over the daemon's lifetime, so a finite
// buffer would deplete. (The old CLI proxy piped the passphrase to stdin
// three times per subprocess for the same reason.)
type passphraseReader struct {
	line []byte
	buf  []byte
}

func (r *passphraseReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		r.buf = append([]byte(nil), r.line...)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// NewKeyring opens the guardian's keyring in-process — the same files
// `timeflared keys` manages, read once with the passphrase from the
// configured file instead of piping it to a subprocess.
func NewKeyring(cfg *config.Config) (keyring.Keyring, error) {
	cdc, _ := newProtoCodec()

	var input io.Reader = os.Stdin
	if cfg.KeyringPassphrase != "" {
		// One passphrase-file reader for both key domains: the file content
		// IS the passphrase (custody.ReadPassphraseFile — raw, never decoded).
		pass, err := custody.ReadPassphraseFile(cfg.KeyringPassphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to read keyring passphrase file: %w", err)
		}
		// A configured passphrase must win even on an interactive terminal.
		suppressKeyringTTYPrompts()
		input = &passphraseReader{line: []byte(pass + "\n")}
	}

	kr, err := keyring.New(sdk.KeyringServiceName(), cfg.KeyringBackend, cfg.KeyringDir, input, cdc)
	if err != nil {
		return nil, fmt.Errorf("failed to open keyring (backend %s, dir %s): %w", cfg.KeyringBackend, cfg.KeyringDir, err)
	}
	return kr, nil
}

// WalletHDPath is the chain's BIP44 derivation path for wallet keys —
// m/44'/ChainCoinType'/0'/0/0, built from the shared constant in
// x/secrets/types (spec.md "Network Configuration",
// CLIENT_CONVENTIONS.md §9). The same path timeflared derives at; never a
// second derivation implementation or a duplicated literal.
func WalletHDPath() string {
	return hd.CreateHDPath(secretstypes.ChainCoinType, 0, 0).String()
}

// CreateWalletKey generates a fresh signing key in the guardian's keyring at
// the chain's HD path, returning its address and 24-word mnemonic (shown
// once — the caller displays it for backup). Refuses to overwrite an
// existing key of the same name.
func CreateWalletKey(cfg *config.Config, name string) (address, mnemonic string, err error) {
	kr, err := NewKeyring(cfg)
	if err != nil {
		return "", "", err
	}
	if _, err := kr.Key(name); err == nil {
		return "", "", fmt.Errorf("key %s already exists in the keyring (backend %s, dir %s) — it will not be overwritten", name, cfg.KeyringBackend, cfg.KeyringDir)
	}
	record, mnemonic, err := kr.NewMnemonic(name, keyring.English, WalletHDPath(), "", hd.Secp256k1)
	if err != nil {
		return "", "", fmt.Errorf("failed to create wallet key %s: %w", name, err)
	}
	addr, err := record.GetAddress()
	if err != nil {
		return "", "", fmt.Errorf("failed to derive address for key %s: %w", name, err)
	}
	return addr.String(), mnemonic, nil
}

// ImportWalletKey restores a signing key from its BIP39 mnemonic at the
// chain's HD path — the pairing timeflared and every client derive
// (pinned by testdata/vectors/wallet_derivation.json). Refuses to overwrite
// an existing key of the same name.
func ImportWalletKey(cfg *config.Config, name, mnemonic string) (string, error) {
	kr, err := NewKeyring(cfg)
	if err != nil {
		return "", err
	}
	if _, err := kr.Key(name); err == nil {
		return "", fmt.Errorf("key %s already exists in the keyring (backend %s, dir %s) — it will not be overwritten", name, cfg.KeyringBackend, cfg.KeyringDir)
	}
	record, err := kr.NewAccount(name, mnemonic, "", WalletHDPath(), hd.Secp256k1)
	if err != nil {
		return "", fmt.Errorf("failed to import wallet key %s: %w", name, err)
	}
	addr, err := record.GetAddress()
	if err != nil {
		return "", fmt.Errorf("failed to derive address for key %s: %w", name, err)
	}
	return addr.String(), nil
}

// ResolveKeyAddress returns the bech32 address of a key in the guardian's
// keyring — the in-process replacement for `timeflared keys show -a`.
func ResolveKeyAddress(cfg *config.Config, keyName string) (string, error) {
	kr, err := NewKeyring(cfg)
	if err != nil {
		return "", err
	}
	return keyAddress(kr, keyName, cfg.KeyringBackend, cfg.KeyringDir)
}

// ResolveKeyAddressWithPassphrase resolves a key's address with an in-memory
// passphrase — used during `config init`, before the passphrase file exists.
func ResolveKeyAddressWithPassphrase(backend, dir, keyName, passphrase string) (string, error) {
	cdc, _ := newProtoCodec()
	// The caller supplied a passphrase — it must win even on a terminal.
	suppressKeyringTTYPrompts()
	kr, err := keyring.New(sdk.KeyringServiceName(), backend, dir, &passphraseReader{line: []byte(passphrase + "\n")}, cdc)
	if err != nil {
		return "", fmt.Errorf("failed to open keyring (backend %s, dir %s): %w", backend, dir, err)
	}
	return keyAddress(kr, keyName, backend, dir)
}

func keyAddress(kr keyring.Keyring, keyName, backend, dir string) (string, error) {
	record, err := kr.Key(keyName)
	if err != nil {
		return "", fmt.Errorf("%w: %s (backend %s, dir %s): %v", ErrKeyNotFound, keyName, backend, dir, err)
	}
	addr, err := record.GetAddress()
	if err != nil {
		return "", fmt.Errorf("failed to derive address for key %s: %w", keyName, err)
	}
	return addr.String(), nil
}
