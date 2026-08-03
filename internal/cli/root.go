package cli

import (
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/config"
)

const appName = "guardiand"

// NewRootCmd creates and returns the root command
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     appName,
		Short:   "Timeflare Guardian Service",
		Version: buildVersion(),
	}

	// Global flags. Nothing is loaded here: each command resolves the
	// configuration it needs (see resolve.go), so a command that needs none —
	// version, help, config init — runs on a host that has none.
	rootCmd.PersistentFlags().String(configFlagName, "",
		"config file path (default is "+config.DefaultConfigRelativePath+")")

	// Add subcommands
	rootCmd.AddCommand(NewStartCmd())
	rootCmd.AddCommand(NewRegisterCmd())
	rootCmd.AddCommand(NewUpdateCmd())
	rootCmd.AddCommand(NewRotateKeyCmd())
	rootCmd.AddCommand(NewStatusCmd())
	rootCmd.AddCommand(NewHealthCmd())
	rootCmd.AddCommand(NewVersionCmd())
	rootCmd.AddCommand(NewConfigCmd())
	rootCmd.AddCommand(NewKeyCmd())
	rootCmd.AddCommand(NewWalletCmd())

	return rootCmd
}
