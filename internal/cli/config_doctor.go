package cli

import (
	"context"

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

	reportNetworkDrift(u, effective)

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

// reportNetworkDrift compares the configured endpoints against the published
// entry they were selected from.
//
// This is the answer to the one thing selection leaves open: values are copied
// into the configuration at setup and never re-read, so a published endpoint that
// moves leaves a guardian on the old one. Re-reading at startup was rejected —
// it would let whoever serves the list redirect a running guardian between
// restarts — which makes reporting it here the alternative to an operator finding
// out by failing.
//
// It never fails the doctor. An operator who deliberately points at their own
// node is not misconfigured, and drift is a difference worth naming rather than a
// fault. Nor does it fail when the list cannot be read: this command has to work
// on a host with no route out.
func reportNetworkDrift(u *ui.Printer, cfg *config.Config) {
	switch cfg.Network {
	case "":
		u.Note("Network: no network recorded — endpoints predate selection, or were set by hand")
		return
	case config.CustomNetworkID:
		u.Note("Network: custom — endpoints are yours, nothing to compare against")
		return
	}

	list, err := config.FetchNetworkList(context.Background(), config.NetworkListSource())
	if err != nil {
		u.Note("Network: %s — not checked (%v)", cfg.Network, err)
		return
	}
	published, ok := list.Find(cfg.Network)
	if !ok {
		u.Note("Network: %s is no longer published — nothing to compare against", cfg.Network)
		return
	}

	drifted := false
	for _, field := range []struct{ name, configured, want string }{
		{"chain_id", cfg.ChainID, published.ChainID},
		{"rpc_endpoint", cfg.RPCEndpoint, published.RPCEndpoint()},
		{"grpc_endpoint", cfg.GRPCEndpoint, published.GRPCEndpoint()},
	} {
		if field.configured != field.want {
			u.Warning("%s is %q; %s publishes %q", field.name, field.configured, cfg.Network, field.want)
			drifted = true
		}
	}
	if drifted {
		u.Note(ui.Indent1 + "Deliberate overrides are fine. If not, 'guardianctl config set' the published values.")
		return
	}
	u.Success("Network: %s matches what the chain publishes (%s)", cfg.Network, published.ChainID)
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
