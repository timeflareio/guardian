package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/config"
)

const (
	appName = "guardiand"
	version = "1.0.0"
)

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
