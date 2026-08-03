package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/config"
)

const appName = "guardiand"

// version is set at build time via
//
//	-ldflags "-X github.com/timeflareio/guardian/cmd/guardiand/cmd.version=vX.Y.Z"
//
// It is deliberately a var, not a const: the linker's -X flag can only write to
// variables, so a const cannot be stamped and would silently keep whatever the
// source says. This previously read "1.0.0" and every release would have shipped
// claiming that, regardless of its tag.
//
// The default is "dev" rather than a version number because this repository does
// not carry its own version — releases do. An unstamped binary is a local build
// and should say so.
var version = "dev"

var (
	configPath string
	cfg        *config.Config
	cfgManager *config.Manager
)

// NewRootCmd creates and returns the root command
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     appName,
		Short:   "Timeflare Guardian Service",
		Version: version,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&configPath, "config-path", "", "config file path (default is $HOME/.timeflare/guardian/config.yaml)")

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

	// Initialize configuration
	cobra.OnInitialize(initConfig)

	return rootCmd
}

// initConfig reads in config file and ENV variables
func initConfig() {
	// Initialize config manager
	cfgManager = config.NewManager(configPath)

	// Load configuration or use defaults
	if err := cfgManager.LoadOrDefault(); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	// Get the loaded config
	cfg = cfgManager.GetConfig()
}
