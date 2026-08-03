package cli

import (
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/config"
)

// Two binaries share this package, and which verbs go where is a custody
// decision rather than a packaging one.
//
// guardiand is the long-running process. It holds the epoch keyring and it is
// the only component with network-facing surface — the dashboard listener and
// the event subscription — so it is the one that benefits from having no code
// path that can mint, export or rewrite key material linked into it at all.
// `key backup` seals the whole epoch keyring into one portable file; that verb
// not being reachable from the daemon is the point of the split.
//
// The claim is precise, and worth not overstating: guardiand still reads the
// signing keyring, because it signs confirmations and reveals, and it still
// decrypts the share key. What it cannot do is write, seal or export one.
//
// make verify checks this rather than trusting it — see verify-daemon-symbols.
const (
	daemonName = "guardiand"
	ctlName    = "guardianctl"
)

// NewGuardiandCmd builds the daemon: run the service, check on it, say what it
// is. Nothing here writes configuration or key material.
func NewGuardiandCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     daemonName,
		Short:   "Timeflare Guardian Service",
		Version: buildVersion(),
		Long: `Run the Timeflare guardian daemon.

Setup and key custody live in ` + ctlName + `: configuration, the signing key,
share-key backup and restore, registration, and key rotation. This binary runs
the service and reports on it, and deliberately carries no code that can write
or export key material.`,
	}
	addConfigPathFlag(root)

	root.AddCommand(NewStartCmd())
	root.AddCommand(NewHealthCmd())
	root.AddCommand(NewVersionCmd())

	return root
}

// NewGuardianctlCmd builds the operator tool: everything that sets a guardian
// up, moves its keys, or asks the chain about it.
func NewGuardianctlCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     ctlName,
		Short:   "Timeflare Guardian operator tool",
		Version: buildVersion(),
		Long: `Set up and maintain a Timeflare guardian.

This is the operator's tool: configuration, the signing key, share-key backup
and restore, registration, and key rotation. The daemon itself is ` + daemonName + `.`,
	}
	addConfigPathFlag(root)

	root.AddCommand(NewConfigCmd())
	root.AddCommand(NewWalletCmd())
	root.AddCommand(NewKeyCmd())
	root.AddCommand(NewRegisterCmd())
	root.AddCommand(NewUpdateCmd())
	root.AddCommand(NewRotateKeyCmd())
	root.AddCommand(NewStatusCmd())
	root.AddCommand(NewVersionCmd())

	return root
}

// addConfigPathFlag declares the one flag both binaries share. Nothing is loaded
// here: each command resolves the configuration it needs (see resolve.go), so a
// command that needs none — version, help, config init — runs on a host that has
// none.
func addConfigPathFlag(root *cobra.Command) {
	root.PersistentFlags().String(configFlagName, "",
		"config file path (default is "+config.DefaultConfigRelativePath+")")
}
