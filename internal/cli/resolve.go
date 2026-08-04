package cli

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
)

// Configuration reaches a command through one function, called explicitly by
// the command that needs it. A resolver that returns its result cannot drift
// from itself: every command holds exactly the view it asked for, a command
// that needs no configuration never loads any, and nothing can replace the
// manager mid-run behind a caller's back.

// configFlagName is the persistent flag naming the configuration file. Declared
// on the root command, so cmd.Flags() resolves it on any subcommand.
const configFlagName = "config-path"

// printer builds this command's output layer over the writers cobra resolved,
// rather than over os.Stdout directly. Taking the writers from the command is
// what lets a test drive a command over a buffer and assert on what it said.
func printer(cmd *cobra.Command) *ui.Printer {
	return ui.New(cmd.OutOrStdout(), cmd.InOrStdin())
}

// newManager builds a manager for the path this invocation asked for.
func newManager(cmd *cobra.Command) *config.Manager {
	path, _ := cmd.Flags().GetString(configFlagName)
	return config.NewManager(path)
}

// requireConfig resolves the configuration for a command that cannot run
// without one: the file, then GUARDIAN_* environment overrides, then any flags
// the command bound with bindConfigFlags. It reports a missing file as an
// error rather than a message-and-success: `guardiand start` on an unconfigured
// host must not exit zero, which to a process supervisor is indistinguishable
// from having run.
func requireConfig(cmd *cobra.Command) (*config.Manager, *config.Config, error) {
	manager := newManager(cmd)
	if !manager.Exists() {
		showNoConfigMessage(printer(cmd), manager.GetConfigPath())
		return nil, nil, errors.Errorf("no configuration at %s", manager.GetConfigPath())
	}
	if err := manager.Load(); err != nil {
		return nil, nil, err
	}
	if err := applyConfigFlags(cmd, manager); err != nil {
		return nil, nil, err
	}
	return manager, manager.GetConfig(), nil
}

// optionalConfig resolves the configuration for a command that can still do
// something useful without one, falling back to defaults plus environment
// overrides. Creating an encryption key into a named directory, or checking a
// health endpoint by URL, needs no configuration file to have been written yet.
func optionalConfig(cmd *cobra.Command) (*config.Manager, *config.Config, error) {
	manager := newManager(cmd)
	if err := manager.LoadOrDefault(); err != nil {
		return nil, nil, err
	}
	if err := applyConfigFlags(cmd, manager); err != nil {
		return nil, nil, err
	}
	return manager, manager.GetConfig(), nil
}

// configFlags records which flags on a command override which configuration
// keys. Keyed by command so sibling commands cannot see each other's bindings.
var configFlags = map[*cobra.Command]map[string]string{}

// bindConfigFlags registers a command-line override for each named
// configuration key, spelling the flag with the key's kebab-case form and
// documenting it from the registry's description. This is what makes the
// precedence the generated YAML advertises — flags > env > file > defaults —
// true rather than aspirational: before this, no command pushed a flag value
// into the configuration at all, so the flag layer did not exist.
//
// Every flag is a string because the registry parses and validates from
// strings; that is also what gives a bad value the same error message
// regardless of whether it arrived by flag, by environment variable or by file.
func bindConfigFlags(cmd *cobra.Command, keys ...string) {
	bound := configFlags[cmd]
	if bound == nil {
		bound = map[string]string{}
		configFlags[cmd] = bound
	}
	for _, key := range keys {
		field, ok := config.LookupField(key)
		if !ok {
			// A typo here is a programming error, not an operator's: fail at
			// construction, where the panic names the offending key, rather
			// than silently shipping a flag that overrides nothing.
			panic("bindConfigFlags: unknown configuration key " + key)
		}
		cmd.Flags().String(field.FlagName, "", field.Description+" (overrides the configuration file)")
		bound[field.FlagName] = field.Key
	}
}

// applyConfigFlags applies the bound flags an invocation actually set.
func applyConfigFlags(cmd *cobra.Command, manager *config.Manager) error {
	for flagName, key := range configFlags[cmd] {
		if !cmd.Flags().Changed(flagName) {
			continue
		}
		value, err := cmd.Flags().GetString(flagName)
		if err != nil {
			return err
		}
		if err := manager.Set(key, value); err != nil {
			return errors.Wrapf(err, "--%s", flagName)
		}
	}
	return nil
}
