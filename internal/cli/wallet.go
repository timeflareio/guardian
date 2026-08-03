package cli

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/cli/ui"

	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
)

// NewWalletCmd creates the wallet command group — lifecycle for the
// guardian's SIGNING key (secp256k1, signs registrations, confirmations and
// reveals). A separate group from 'guardianctl key', which is the share-key
// (X25519) domain: one verb per key domain, so 'wallet create' can never be
// mistaken for creating a share key. Before this group existed the
// distroless guardian image once pointed operators at 'timeflared keys add' — a
// binary the image does not ship.
func NewWalletCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wallet",
		Short: "Manage the guardian's signing (wallet) key",
		Long: `Manage the guardian's SIGNING key — the secp256k1 key that signs
registrations, confirmations and reveals, held in the cosmos keyring the
daemon already uses (keyring_backend / keyring_dir / keyring_passphrase in
config).

This is one of the guardian's two key domains:
  wallet — the signing key (this group): create, import, show-address
  key    — the X25519 share-encryption keys: backup, restore

Keys derive at the chain's HD path (m/44'/9733'/0'/0/0 — spec.md "Network
Configuration"), the same derivation as timeflared and every wallet client:
the 24-word mnemonic restores the same account everywhere.`,
	}

	cmd.AddCommand(NewWalletCreateCmd())
	cmd.AddCommand(NewWalletImportCmd())
	cmd.AddCommand(NewWalletShowAddressCmd())

	return cmd
}

// The wallet commands resolve their configuration optionally, not by
// requirement: the setup order this daemon documents is `wallet create` first,
// `config init` second — init resolves the guardian's address from the signing
// key, so the key has to exist before there is a configuration file to demand.
// Keyring backend and directory fall back to their defaults, which is what a
// first key creation wants anyway.

// walletKeyName resolves the key name: --name flag, else the configured
// key_name.
func walletKeyName(cmd *cobra.Command, effective *config.Config) (string, error) {
	name, _ := cmd.Flags().GetString("name")
	if name != "" {
		return name, nil
	}
	if effective.KeyName == "" {
		return "", errors.New("no key name: pass --name or set key_name in config")
	}
	return effective.KeyName, nil
}

// NewWalletCreateCmd creates the wallet create command.
func NewWalletCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Generate a fresh signing key and show its backup mnemonic",
		Long: `Generate a fresh signing key in the guardian's keyring at the chain's HD
path. The 24-word mnemonic is shown ONCE — write it down and store it
securely; it is the only way to recover the key and any balance it holds.`,
		RunE: runWalletCreate,
	}
	cmd.Flags().String("name", "", "key name (default: key_name from config)")
	return cmd
}

func runWalletCreate(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	_, effective, err := optionalConfig(cmd)
	if err != nil {
		return err
	}
	name, err := walletKeyName(cmd, effective)
	if err != nil {
		return err
	}

	address, mnemonic, err := chain.CreateWalletKey(effective, name)
	if err != nil {
		return err
	}

	u.EmptyLine()
	u.Success("Created signing key %s", name)
	u.Text(ui.Indent1 + "Address: ")
	u.Command("%s\n", address)
	u.Text(ui.Indent1 + "HD path: ")
	u.Command("%s\n", chain.WalletHDPath())
	u.EmptyLine()
	u.Note("Backup mnemonic — shown ONCE, never stored by the daemon:")
	u.EmptyLine()
	u.TextLn(ui.Indent1 + mnemonic)
	u.EmptyLine()
	u.Note("Write the 24 words down and store them securely. Anyone holding")
	u.Note("them controls this key's balance; without them a lost keyring is")
	u.Note("unrecoverable. The same words restore the same account in any")
	u.Note("Timeflare wallet ('guardianctl wallet import-from-mnemonic',")
	u.Note("'timeflared keys add --recover', or the mobile app).")
	u.EmptyLine()
	return nil
}

// NewWalletImportCmd creates the wallet import-from-mnemonic command.
func NewWalletImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import-from-mnemonic",
		Short: "Restore a signing key from its 24-word mnemonic",
		Long: `Restore a signing key from its BIP39 mnemonic at the chain's HD path —
the same derivation as timeflared and every wallet client, so the words
resolve to the same account everywhere.`,
		RunE: runWalletImport,
	}
	cmd.Flags().String("name", "", "key name (default: key_name from config)")
	return cmd
}

func runWalletImport(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	_, effective, err := optionalConfig(cmd)
	if err != nil {
		return err
	}
	name, err := walletKeyName(cmd, effective)
	if err != nil {
		return err
	}

	mnemonic := u.PromptInput(ui.Indent1 + "Enter the 24-word mnemonic: ")
	if mnemonic == "" {
		return errors.New("no mnemonic entered")
	}

	address, err := chain.ImportWalletKey(effective, name, mnemonic)
	if err != nil {
		return err
	}

	u.EmptyLine()
	u.Success("Imported signing key %s", name)
	u.Text(ui.Indent1 + "Address: ")
	u.Command("%s\n", address)
	u.EmptyLine()
	u.Note("Verify this address is the one you expected — a wrong or")
	u.Note("mistyped phrase derives a different, empty account.")
	u.EmptyLine()
	return nil
}

// NewWalletShowAddressCmd creates the wallet show-address command.
func NewWalletShowAddressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show-address",
		Short: "Show the signing key's address",
		Long: `Show the bech32 address of the guardian's signing key — the in-process
replacement for 'timeflared keys show -a'.`,
		RunE: runWalletShowAddress,
	}
	cmd.Flags().String("name", "", "key name (default: key_name from config)")
	return cmd
}

func runWalletShowAddress(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	_, effective, err := optionalConfig(cmd)
	if err != nil {
		return err
	}
	name, err := walletKeyName(cmd, effective)
	if err != nil {
		return err
	}

	address, err := chain.ResolveKeyAddress(effective, name)
	if err != nil {
		return err
	}
	u.Printf("%s\n", address)
	return nil
}
