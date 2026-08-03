package cmd

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/timeflareio/guardian/blockchain"
)

// NewWalletCmd creates the wallet command group — lifecycle for the
// guardian's SIGNING key (secp256k1, signs registrations, confirmations and
// reveals). A separate group from 'guardiand key', which is the share-key
// (X25519) domain: one verb per key domain, so 'wallet create' can never be
// mistaken for creating a share key. Before this group existed the
// distroless guardian image pointed operators at 'timeflared keys add' — a
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

// walletKeyName resolves the key name: --name flag, else the configured
// key_name.
func walletKeyName(cmd *cobra.Command) (string, error) {
	name, _ := cmd.Flags().GetString("name")
	if name != "" {
		return name, nil
	}
	effective := cfgManager.GetConfig()
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
	if err := cfgManager.Load(); err != nil {
		return err
	}
	name, err := walletKeyName(cmd)
	if err != nil {
		return err
	}

	address, mnemonic, err := blockchain.CreateWalletKey(cfgManager.GetConfig(), name)
	if err != nil {
		return err
	}

	printEmptyLine()
	printSuccess("Created signing key %s", name)
	printText(indent1 + "Address: ")
	printCommand("%s\n", address)
	printText(indent1 + "HD path: ")
	printCommand("%s\n", blockchain.WalletHDPath())
	printEmptyLine()
	printNote("Backup mnemonic — shown ONCE, never stored by the daemon:")
	printEmptyLine()
	printTextLn(indent1 + mnemonic)
	printEmptyLine()
	printNote("Write the 24 words down and store them securely. Anyone holding")
	printNote("them controls this key's balance; without them a lost keyring is")
	printNote("unrecoverable. The same words restore the same account in any")
	printNote("Timeflare wallet ('guardiand wallet import-from-mnemonic',")
	printNote("'timeflared keys add --recover', or the mobile app).")
	printEmptyLine()
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
	if err := cfgManager.Load(); err != nil {
		return err
	}
	name, err := walletKeyName(cmd)
	if err != nil {
		return err
	}

	mnemonic := promptForInput(indent1 + "Enter the 24-word mnemonic: ")
	if mnemonic == "" {
		return errors.New("no mnemonic entered")
	}

	address, err := blockchain.ImportWalletKey(cfgManager.GetConfig(), name, mnemonic)
	if err != nil {
		return err
	}

	printEmptyLine()
	printSuccess("Imported signing key %s", name)
	printText(indent1 + "Address: ")
	printCommand("%s\n", address)
	printEmptyLine()
	printNote("Verify this address is the one you expected — a wrong or")
	printNote("mistyped phrase derives a different, empty account.")
	printEmptyLine()
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
	if err := cfgManager.Load(); err != nil {
		return err
	}
	name, err := walletKeyName(cmd)
	if err != nil {
		return err
	}

	address, err := blockchain.ResolveKeyAddress(cfgManager.GetConfig(), name)
	if err != nil {
		return err
	}
	printf("%s\n", address)
	return nil
}
