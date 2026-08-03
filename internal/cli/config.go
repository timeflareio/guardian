package cli

import (
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
)

// ensureGuardianDirectory creates the guardian directory if it doesn't exist
func ensureGuardianDirectory(manager *config.Manager) error {
	// Use the config manager to get the correct key directory
	return manager.EnsureDirectoriesExist()
}

// showNoConfigMessage displays a consistent "no configuration found" message
func showNoConfigMessage(u *ui.Printer, configPath string) {
	u.EmptyLine()
	u.Separator("No Configuration Found!")
	u.EmptyLine()

	u.Note("Guardian operations require a configuration file")
	u.Text(ui.Indent1 + "Expected config file: ")
	u.Path("%s\n", configPath)

	u.Text(ui.Indent1 + "Run: ")
	u.Command("guardianctl config init")
	u.TextLn(" to create the configuration file.")

	u.Text(ui.Indent1 + "Or use: ")
	u.Command("--config-path /path/to/config.yaml")
	u.TextLn(" to specify a custom location.\n")

	u.Note("The configuration setup will guide you through:")
	u.TextLn(ui.Indent1 + "• Setting up your guardian signing key")
	u.TextLn(ui.Indent1 + "• Generating encryption keys for secret shares")
	u.TextLn(ui.Indent1 + "• Configuring blockchain connection settings\n")
}

// NewConfigCmd creates the config command
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage guardian configuration",
		Long: `Manage guardian configuration settings.

Configuration is stored in ~/.timeflare/guardian/config.yaml by default.
Use --config-path to specify a different location.`,
		RunE: runConfigHelp,
	}

	// Add subcommands
	cmd.AddCommand(NewConfigInitCmd())
	cmd.AddCommand(NewConfigSetCmd())
	cmd.AddCommand(NewConfigGetCmd())
	cmd.AddCommand(NewConfigListCmd())
	cmd.AddCommand(NewConfigValidateCmd())
	cmd.AddCommand(NewConfigDoctorCmd())
	cmd.AddCommand(NewConfigCreateEncryptionKeyCmd())
	cmd.AddCommand(NewConfigMigrateKeyCmd())
	cmd.AddCommand(NewConfigSetDashboardPasswordCmd())

	return cmd
}

// runConfigHelp shows help and checks if config exists. It resolves its own
// manager rather than requiring one: the whole point of this command is to be
// useful on a host that has no configuration yet.
func runConfigHelp(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	manager := newManager(cmd)
	configPath := manager.GetConfigPath()

	if !manager.Exists() {
		showNoConfigMessage(u, configPath)
		return nil
	}

	u.EmptyLine()
	u.Note("Configuration found:")
	u.Text(ui.Indent1 + "✓ Config: ")
	u.Command("%s\n", configPath)
	u.EmptyLine()
	u.Text(ui.Indent1 + "Use: ")
	u.Command("guardianctl config list")
	u.EmptyLine()
	u.EmptyLine()

	// Show normal help
	return cmd.Help()
}

// NewConfigSetCmd creates the config set command
func NewConfigSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set a configuration value for the specified key.

Use 'guardianctl config list' to see all available configuration parameters.
Parameters can be specified using either underscore (chain_id) or hyphen (chain-id) format.`,
		Example: `  # Set chain ID
  guardianctl config set chain-id timeflare-1

  # Set guardian key name
  guardianctl config set key-name guardian

  # Set RPC endpoint
  guardianctl config set rpc-endpoint http://localhost:26657

  # Set stake amount
  guardianctl config set stake-amount 15000uveil

  # Set numeric values
  guardianctl config set retry-attempts 5

  # Set boolean values
  guardianctl config set enable-metrics false`,
		Args: cobra.ExactArgs(2),
		RunE: runConfigSet,
	}

	return cmd
}

// NewConfigGetCmd creates the config get command
func NewConfigGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Long:  `Get the current value for the specified configuration key.`,
		Example: `  # Get chain ID
  guardianctl config get chain-id

  # Get key name
  guardianctl config get key-name`,
		Args: cobra.ExactArgs(1),
		RunE: runConfigGet,
	}

	return cmd
}

// NewConfigListCmd creates the config list command
func NewConfigListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all configuration values",
		Long:  `Display all current configuration key-value pairs.`,
		Example: `  # List all configuration
  guardianctl config list`,
		RunE: runConfigList,
	}

	return cmd
}

// NewConfigValidateCmd creates the config validate command
func NewConfigValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration",
		Long: `Validate the current configuration for completeness and correctness.

This checks:
- All required fields are present
- Values are in correct format
- Guardian key exists in timeflared keyring (if possible)`,
		Example: `  # Validate current configuration
  guardianctl config validate`,
		RunE: runConfigValidate,
	}

	return cmd
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	key := args[0]
	value := args[1]

	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist for modifications
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Set the value
	if err := manager.Set(key, value); err != nil {
		return errors.Wrap(err, "failed to set config")
	}

	// Save the config
	if err := manager.Save(); err != nil {
		return errors.Wrap(err, "failed to save config")
	}

	u.Printf("Set %s = %s\n\n", key, value)
	return nil
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	key := args[0]

	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Get the value
	value, err := manager.Get(key)
	if err != nil {
		return err
	}

	u.TextLn(value + "\n")
	return nil
}

func runConfigList(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Get grouped configuration
	groups := manager.ListAllGrouped()

	// Print header
	u.Header("Guardian Configuration")
	u.Text("Config file: ")
	u.Path("%s\n\n", manager.GetConfigPath())

	// Define group order for consistent display
	groupOrder := []string{
		"Network Configuration",
		"Guardian Identity & Keys",
		"Staking & Economics",
		"Networking & Timeouts",
		"Guardian Service",
		"Registration Defaults",
		"Blockchain Integration",
		"Monitoring & Observability",
		"File Paths & Storage",
		"Performance",
		"Operational",
	}

	for i, groupName := range groupOrder {
		group, exists := groups[groupName]
		if !exists || len(group) == 0 {
			continue
		}

		// Print group header
		u.Header("▶ %s", groupName)

		// Sort keys within group for consistent display
		var keys []string
		for key := range group {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		// Print each configuration item
		for _, key := range keys {
			item := group[key]
			value := item.Value

			// Format empty values nicely - we'll handle this inline

			// Truncate very long values for display
			displayValue := value
			if len(value) > 60 {
				displayValue = value[:57] + "..."
			}

			// Calculate actual display length (without color codes)
			actualLen := 2 + 25 + 3 + len(ui.StripANSI(displayValue)) // "  " + key + " = " + value

			// Print key-value pair with indentation
			u.Text("  ")
			u.Key("%-25s", key)
			u.Text(" = ")
			if value == "" {
				u.Text("(empty)")
			} else {
				u.Value("%s", displayValue)
			}

			// Add description aligned at column 90
			targetCol := 90
			if actualLen < targetCol {
				spaces := targetCol - actualLen
				u.Printf("%*s", spaces, "")
			} else {
				u.Text("  ")
			}

			descText := strings.ToLower(item.Description)
			if len(descText) > 70 {
				descText = descText[:67] + "..."
			}
			u.Printf("# %s", descText)
			u.EmptyLine()
		}

		// Add spacing between groups (except for last group)
		if i < len(groupOrder)-1 {
			u.EmptyLine()
		}
	}

	u.Printf("\nUse 'guardianctl config set <key> <value>' to change values.\n")
	u.Printf("Use 'guardianctl config get <key>' to view a specific value.\n\n")

	return nil
}

func runConfigValidate(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	// Use the global config manager (respects --config-path flag)
	// Load current config - require file to exist
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Validate config
	if err := manager.Validate(); err != nil {
		return errors.Wrap(err, "configuration validation failed")
	}

	u.TextLn("Configuration is valid ✓\n")
	return nil
}
