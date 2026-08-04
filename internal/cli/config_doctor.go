package cli

import (
	"sort"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
)

// `config doctor` reports the configuration as the running service would
// consume it, then checks that the pieces the daemon needs actually resolve.

// NewConfigDoctorCmd creates the config doctor command
func NewConfigDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report effective configuration and check operational readiness",
		Long: `Report the configuration exactly as the running service would consume it
(file + GUARDIAN_* environment overrides, typed effective values), then check:
- cross-field validity (ports, endpoints, durations)
- the signing key resolves in the keyring
- the share-decryption private key loads`,
		RunE: runConfigDoctor,
	}
}

func runConfigDoctor(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	manager, _, err := requireConfig(cmd)
	if err != nil {
		return err
	}
	effective := manager.GetConfig()

	u.EmptyLine()
	u.Separator("🩺 Guardian Configuration Doctor")
	u.EmptyLine()
	u.Note("Effective values (file + environment overrides), as the service consumes them:")
	u.EmptyLine()

	grouped := manager.ListAllGrouped()
	for _, group := range config.GroupOrder() {
		items := grouped[group]
		keys := make([]string, 0, len(items))
		for key := range items {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		u.Header("%s:", group)
		for _, key := range keys {
			item := items[key]
			marker := " "
			if !item.IsDefault {
				marker = "*"
			}
			u.Printf(ui.Indent1+"%s %-28s %s\n", marker, key, item.Value)
		}
		u.EmptyLine()
	}
	u.Note("(* = differs from default)")
	u.EmptyLine()

	failed := false

	if err := effective.Validate(); err != nil {
		u.Error("Validation: %v", err)
		failed = true
	} else {
		u.Success("Validation: configuration is consistent")
	}

	if address, err := chain.ResolveKeyAddress(effective, effective.KeyName); err != nil {
		u.Error("Signing key: %v", err)
		failed = true
	} else {
		u.Success("Signing key: %s resolves to %s", effective.KeyName, address)
		if effective.GuardianAddress != "" && effective.GuardianAddress != address {
			u.Error("guardian_address (%s) does not match the key's address (%s)", effective.GuardianAddress, address)
			failed = true
		}
	}

	if _, err := effective.GetEncryptionPrivateKey(); err != nil {
		u.Error("Encryption key: %v", err)
		failed = true
	} else {
		u.Success("Encryption key: loads from %s", effective.EncryptionPrivateKeyPath)
	}

	reportDashboardExposure(u, effective)

	u.EmptyLine()
	if failed {
		return errors.New("configuration doctor found problems")
	}
	u.Success("Guardian configuration is operational ✓")
	u.EmptyLine()
	return nil
}
