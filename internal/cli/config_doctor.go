package cli

import (
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
	var configOnly bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report effective configuration and check operational readiness",
		Long: `Report the configuration exactly as the running service would consume it
(file + GUARDIAN_* environment overrides, typed effective values), then check:
- cross-field validity (ports, endpoints, durations)
- the signing key resolves in the keyring
- the share-decryption private key loads

--config-only stops after the first of those, for a host where the key material
is not expected to be present yet.`,
		Example: `  # Full report
  guardianctl config doctor

  # Configuration consistency alone, without touching key material
  guardianctl config doctor --config-only`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigDoctor(cmd, configOnly)
		},
	}

	cmd.Flags().BoolVar(&configOnly, "config-only", false, "check configuration consistency only, skipping the signing and share key checks")

	return cmd
}

func runConfigDoctor(cmd *cobra.Command, configOnly bool) error {
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

	groups := orderedConfigGroups(manager)
	keyWidth := 0
	for _, group := range groups {
		for _, key := range group.Keys {
			keyWidth = max(keyWidth, len(key))
		}
	}
	for _, group := range groups {
		u.Header("%s:", group.Name)
		for _, key := range group.Keys {
			item := group.Items[key]
			marker := " "
			if !item.IsDefault {
				marker = "*"
			}
			u.Printf(ui.Indent1+"%s %-*s %s\n", marker, keyWidth, key, item.Value)
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

	// --config-only stops here: everything above reads the configuration, and
	// everything below reaches for key material that a host being prepared may
	// not hold yet.
	if configOnly {
		reportDashboard(u, effective)
		u.EmptyLine()
		if failed {
			return errors.New("configuration doctor found problems")
		}
		u.Success("Guardian configuration is consistent ✓")
		u.EmptyLine()
		return nil
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

	reportDashboard(u, effective)

	u.EmptyLine()
	if failed {
		return errors.New("configuration doctor found problems")
	}
	u.Success("Guardian configuration is operational ✓")
	u.EmptyLine()
	return nil
}

// reportDashboard states where the operator page will be served and what it
// will and will not say.
//
// It never fails the doctor. The dashboard carries no credential and nothing
// confidential, so there is no misconfiguration left for it to be wrong about —
// only a port an operator may not have meant to publish, which is worth naming
// once and is not a reason to call the guardian unhealthy.
func reportDashboard(u *ui.Printer, cfg *config.Config) {
	if !cfg.EnableDashboard {
		u.Note("Dashboard: disabled (enable_dashboard is false)")
		return
	}
	u.Success("Dashboard: read-only on %s:%d, no credential", cfg.BindAddress, cfg.DashboardPort)
	u.Note("It serves what the chain already publishes about this guardian, plus liveness — no paths, endpoints or key custody detail.")
	u.Note("Anything that changes the guardian is guardianctl on this host, never the page.")
}
