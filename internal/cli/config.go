package cli

import (
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
)

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
		// Without this a mistyped or retired subcommand — `config validate`,
		// which this command used to have — is swallowed as an argument and
		// answered with the help text and exit zero, which in a script is
		// indistinguishable from having run.
		Args: cobra.NoArgs,
		RunE: runConfigHelp,
	}

	// Add subcommands
	cmd.AddCommand(NewConfigInitCmd())
	cmd.AddCommand(NewConfigSetCmd())
	cmd.AddCommand(NewConfigGetCmd())
	cmd.AddCommand(NewConfigListCmd())
	cmd.AddCommand(NewConfigDoctorCmd())
	cmd.AddCommand(NewConfigCreateEncryptionKeyCmd())
	cmd.AddCommand(NewConfigMigrateKeyCmd())

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

	groups := orderedConfigGroups(manager)

	u.Header("Guardian Configuration")
	u.Text("Config file: ")
	u.Path("%s\n\n", manager.GetConfigPath())

	// Column widths come from the data rather than from guessed constants. The
	// guesses were 25 for the key and column 90 for the description, and a key
	// longer than 25 (encryption_private_key_path is 27) shifted its whole row
	// out of alignment. Padding inside the coloured write also removes the need
	// to measure a string with escape codes in it.
	keyWidth, valueWidth := 0, 0
	for _, group := range groups {
		for _, key := range group.Keys {
			keyWidth = max(keyWidth, len(key))
			valueWidth = max(valueWidth, len(listValue(group.Items[key].Value)))
		}
	}

	for i, group := range groups {
		u.Header("▶ %s", group.Name)
		for _, key := range group.Keys {
			item := group.Items[key]
			u.Text("  ")
			u.Key("%-*s", keyWidth, key)
			u.Text(" = ")
			u.Value("%-*s", valueWidth, listValue(item.Value))
			u.Printf("  # %s\n", strings.ToLower(item.Description))
		}
		// Add spacing between groups (except for last group)
		if i < len(groups)-1 {
			u.EmptyLine()
		}
	}

	u.Printf("\nUse 'guardianctl config set <key> <value>' to change values.\n")
	u.Printf("Use 'guardianctl config get <key>' to view a specific value.\n\n")

	return nil
}

// configGroup is one section of the configuration, ready to render: the group's
// name, its keys in display order, and the items behind them.
type configGroup struct {
	Name  string
	Keys  []string
	Items map[string]config.ConfigItem
}

// orderedConfigGroups walks the configuration in the registry's own group order
// with each group's keys sorted. Both `config list` and `config doctor` render
// from this, so a group or field added to the registry reaches both without
// either being touched — and neither can carry a stale copy of the group names,
// which is how `config list` once came to print nothing at all.
func orderedConfigGroups(manager *config.Manager) []configGroup {
	grouped := manager.ListAllGrouped()
	groups := make([]configGroup, 0, len(grouped))
	for _, name := range config.GroupOrder() {
		items := grouped[name]
		if len(items) == 0 {
			continue
		}
		keys := make([]string, 0, len(items))
		for key := range items {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		groups = append(groups, configGroup{Name: name, Keys: keys, Items: items})
	}
	return groups
}

// listValue is how a value reads in the listing: an unset one says so rather
// than leaving the column blank.
func listValue(value string) string {
	if value == "" {
		return "(empty)"
	}
	return value
}
