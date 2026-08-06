package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/guardian"
	"github.com/timeflareio/guardian/internal/monitoring"
)

// entryFee renders the one-off registration fee as the wire contract states it,
// in the base units every other amount in this configuration takes. Derived
// rather than written out: a figure copied into operator-facing text is wrong the
// moment a chain upgrade retunes it, and wrong in the place an operator is most
// likely to trust it.
func entryFee() string {
	return strconv.FormatInt(secretstypes.EntryFeeAmount, 10) + secretstypes.DefaultDenom
}

// NewRegisterCmd creates the register command
func NewRegisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register guardian with the network (new registrations only)",
		Long: `Register a new guardian with the Timeflare network.

The registration transaction is built, signed with your keyring key, and
broadcast in-process — the exact parameters are shown and confirmed before
anything is sent.

IMPORTANT: This command only handles NEW registrations. Use 'guardianctl update'
for changes to an existing registration.

Registration charges the protocol entry fee (` + entryFee() + `, routed through the
chain's fee split) in addition to the float deposit, and the fee is never
returned. The deposit is your working capital: a bond
(rate × distance × bump × your bond multiplier k) is locked from it for each secret you accept
and returned at settlement.

Parameters (config-default + flag-override):
- stake-amount: Initial float deposit (defaults to the configured stake_amount)
- available-until: Blocks from current when guardian stops being available
  (defaults to the chain's maximum availability window)
- available-from: Blocks from current when guardian becomes available (default: 0 = immediate)
- accepting-secrets: Whether guardian accepts new secret assignments (default: true)

Note: Uses encryption public key from configuration file.`,
		Example: `  # Registration with config defaults
  guardianctl register

  # Explicit deposit and availability window
  guardianctl register --stake-amount 10000000000uveil --available-until 14400

  # Registration NOT accepting secrets initially
  guardianctl register --stake-amount 15000000000uveil --available-until 28800 --accepting-secrets=false`,
		RunE:         runRegister,
		SilenceUsage: true, // Don't show usage on errors
	}

	// Command-specific flags (all optional — config defaults apply)
	cmd.Flags().String("stake-amount", "", "initial float deposit (default: configured stake_amount; the "+entryFee()+" entry fee is charged separately and routed through the chain's fee split)")
	cmd.Flags().Int64("available-from", 0, "blocks from current when guardian becomes available (default: 0 = immediate)")
	cmd.Flags().String("available-until", "", "blocks from current when guardian stops being available (default: chain maximum)")
	cmd.Flags().Bool("accepting-secrets", true, "whether guardian accepts new secret assignments (default: true)")
	cmd.Flags().Bool("accept", false, "automatically accept and execute without prompting")
	cmd.Flags().Bool("skip-service-check", false, "register without checking that guardiand is running (for a daemon started separately, or on another host)")

	return cmd
}

func runRegister(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	_, cfg, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Flags override config defaults
	stakeAmount, _ := cmd.Flags().GetString("stake-amount")
	if stakeAmount == "" {
		stakeAmount = cfg.StakeAmount
	}
	availableUntilStr, _ := cmd.Flags().GetString("available-until")
	availableFrom, _ := cmd.Flags().GetInt64("available-from")
	acceptingSecrets, _ := cmd.Flags().GetBool("accepting-secrets")
	autoAccept, _ := cmd.Flags().GetBool("accept")
	skipServiceCheck, _ := cmd.Flags().GetBool("skip-service-check")

	availableUntil := int64(secretstypes.MaxAvailabilityWindow)
	if availableUntilStr != "" {
		var err error
		availableUntil, err = strconv.ParseInt(availableUntilStr, 10, 64)
		if err != nil {
			return errors.Wrap(err, "invalid available-until value")
		}
	}

	// Validate parameters
	encryptionKeyHex := cfg.EncryptionPublicKey
	if encryptionKeyHex == "" {
		return errors.New("encryption public key is required. Set it in config with 'guardianctl config set encryption-public-key <key>'")
	}
	if len(encryptionKeyHex) != 64 {
		return errors.Errorf("encryption public key must be exactly 64 hex characters (32 bytes), got %d characters", len(encryptionKeyHex))
	}
	if availableUntil <= availableFrom {
		return errors.Errorf("available-until (%d) must be greater than available-from (%d)", availableUntil, availableFrom)
	}

	// Pre-flight: the key must exist in the keyring (in-process check)
	guardianAddress, err := getGuardianAddress(cfg)
	if err != nil {
		u.EmptyLine()
		u.Error("Guardian key not accessible: %v", err)
		u.Text(ui.Indent1 + "Create it with: ")
		u.Command("guardianctl wallet create\n")
		u.Text(ui.Indent1 + "Or restore it with: ")
		u.Command("guardianctl wallet import-from-mnemonic\n")
		u.EmptyLine()
		return errors.New("guardian key not found")
	}

	// Registration makes this guardian a selection candidate from the next
	// block, so the daemon wants to be up before the transaction lands rather
	// than after it. Checked before the preview, because "start it first" is
	// advice an operator can still act on at that point.
	if !skipServiceCheck && !autoAccept && acceptingSecrets {
		if !confirmServiceRunning(cmd, u, cfg) {
			u.Note("Registration cancelled — start the service with 'guardiand start' and run this again.")
			u.EmptyLine()
			return nil
		}
	}

	// Show the registration and either execute or bail
	if !autoAccept && !showRegistrationAndConfirm(u, cfg, guardianAddress, stakeAmount, availableFrom, availableUntil, acceptingSecrets) {
		u.Note("Registration cancelled.")
		u.EmptyLine()
		return nil
	}

	logger, err := initLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return errors.Wrap(err, "failed to initialize logger")
	}
	defer func() { _ = logger.Sync() }()

	// The registration manager directly, not a whole guardian.Service: this verb
	// needs one message signed and broadcast, and building the Service would drag
	// the event monitor, the secret cache and the reveal loop in behind it. That
	// mattered little in one binary and matters in two — `update` has always done
	// it this way.
	client, err := chain.NewClient(cfg, logger)
	if err != nil {
		return errors.Wrap(err, "failed to initialise chain client")
	}
	defer func() { _ = client.Close() }()

	registration := guardian.NewRegistrationManager(cfg, client, logger)
	if err := registration.RegisterWithOptions(cmd.Context(), &guardian.RegistrationOptions{
		StakeAmount:            stakeAmount,
		AvailableFrom:          availableFrom,
		AvailableUntil:         availableUntil,
		EncryptionPublicKeyHex: encryptionKeyHex,
	}); err != nil {
		return errors.Wrap(err, "registration failed")
	}

	showRegistrationSuccess(u, cfg, availableFrom, availableUntil, acceptingSecrets)
	return nil
}

// serviceCheckTimeout bounds the health probe. Short on purpose: this answers
// "is the daemon up on this host", and an operator waiting on a registration
// should not sit through a long timeout to be told what a refused connection
// says immediately.
const serviceCheckTimeout = 3 * time.Second

// confirmServiceRunning probes the local health endpoint and, when the daemon
// does not answer, states what registering without it costs and asks whether to
// go ahead. Returns whether registration should continue.
//
// A guardian is a selection candidate from the block its registration lands in.
// Nothing waits for the daemon to come up: assignments arrive, their commit
// deadlines run, and shares nobody confirmed are shares nobody reveals — which
// is slashed. The gap between registering and starting is the whole exposure,
// and it is silent.
func confirmServiceRunning(cmd *cobra.Command, u *ui.Printer, cfg *config.Config) bool {
	url := fmt.Sprintf("http://localhost:%d", cfg.HealthPort)
	if _, err := monitoring.CheckHealth(cmd.Context(), url, serviceCheckTimeout); err == nil {
		return true
	}

	u.EmptyLine()
	u.Warning("The guardian service is not answering on %s", url)
	u.EmptyLine()
	u.TextLn(ui.Indent1 + "Registering makes this guardian a selection candidate from the next block.")
	u.TextLn(ui.Indent1 + "Assignments can arrive before the service is up, and a share that is never")
	u.TextLn(ui.Indent1 + "confirmed or revealed is slashed — the daemon is what does both.")
	u.EmptyLine()
	u.Text(ui.Indent1 + "Start it first with: ")
	u.Command("guardiand start\n")
	u.TextLn(ui.Indent1 + "Then run this command again.")
	u.EmptyLine()
	u.Note(ui.Indent1 + "If the daemon runs elsewhere, or you intend to start it shortly, continuing is fine.")
	u.Note(ui.Indent1 + "Use --skip-service-check to stop being asked.")
	u.EmptyLine()

	return u.Confirm("Register anyway, without the service running?")
}

// showRegistrationAndConfirm displays the registration parameters and asks
// for confirmation before the transaction is signed and broadcast.
func showRegistrationAndConfirm(u *ui.Printer, cfg *config.Config, guardianAddress, stakeAmount string, availableFrom, availableUntil int64, acceptingSecrets bool) bool {
	u.EmptyLine()
	u.Separator("🚀 Guardian Registration Preview")
	u.EmptyLine()

	u.Header("📋 Registration Details:")
	u.EmptyLine()

	u.Text(ui.Indent1 + "• Guardian Key:        ")
	u.Value("%s\n", cfg.KeyName)

	u.Text(ui.Indent1 + "• Guardian Address:    ")
	u.Value("%s\n", guardianAddress)

	u.Text(ui.Indent1 + "• Float Deposit:       ")
	u.Value("%s\n", stakeAmount)

	u.Text(ui.Indent1 + "• Available From:      ")
	if availableFrom > 0 {
		u.Value("Current block + %d\n", availableFrom)
	} else {
		u.Value("Current block + 1\n")
	}

	u.Text(ui.Indent1 + "• Available Until:     ")
	u.Value("Current block + %d\n", availableUntil)

	u.Text(ui.Indent1 + "• Duration:            ")
	u.Value("~%d blocks\n", availableUntil-availableFrom)

	u.Text(ui.Indent1 + "• Accepting Secrets:   ")
	if acceptingSecrets {
		u.Value("Yes (accepting new assignments)\n")
	} else {
		u.Note("No (not accepting new assignments)\n")
	}

	u.EmptyLine()
	u.Header("⚠️  This will:")
	u.TextLn(ui.Indent1 + "• Sign MsgGuardianRegister with your keyring key and broadcast it")
	u.TextLn(ui.Indent1 + "• Burn the 1,000 VEIL entry fee and deposit the specified float from your account")
	u.TextLn(ui.Indent1 + "• Make your guardian available for the specified duration")
	u.TextLn(ui.Indent1 + "• You must run 'guardiand start' to begin actively handling assignments")
	u.EmptyLine()

	return u.Confirm("Execute this registration?")
}

// showRegistrationSuccess displays success message and next steps
func showRegistrationSuccess(u *ui.Printer, cfg *config.Config, availableFrom, availableUntil int64, acceptingSecrets bool) {
	headerColor := color.New(color.FgGreen, color.Bold)
	sectionColor := color.New(color.FgYellow, color.Bold)
	commandColor := color.New(color.FgGreen, color.Bold)
	valueColor := color.New(color.FgCyan)

	u.EmptyLine()
	headerColor.Println("✅ Guardian Registration Successful!")
	headerColor.Println("═══════════════════════════════════")
	u.EmptyLine()

	sectionColor.Println("📅 Availability Window:")
	u.EmptyLine()
	u.Text("   • Status:     ")
	valueColor.Println("Registered and ready for assignments")
	u.Text("   • Duration:   ")
	valueColor.Printf("%d blocks\n", availableUntil-availableFrom)
	u.EmptyLine()

	sectionColor.Println("🚀 Next Steps:")
	u.EmptyLine()
	u.TextLn("   Start your guardian service to begin accepting secret assignments:")
	u.Text("   ")
	commandColor.Println("guardiand start")
	u.EmptyLine()

	sectionColor.Println("⚠️  Important Reminders:")
	u.EmptyLine()
	if acceptingSecrets {
		u.TextLn("   • Your guardian is ELIGIBLE to receive new secret assignments")
		u.TextLn("   • Run 'guardiand start' to begin actively handling assignments")
		u.TextLn("   • Missing reveals while assigned will result in slashing penalties")
	} else {
		u.TextLn("   • Your guardian is NOT eligible to receive new secret assignments")
		u.TextLn("   • To become eligible, run 'guardianctl update --accepting-secrets=true'")
	}
	u.Printf("   • Monitor your guardian's health at http://localhost:%d/health\n", cfg.HealthPort)
	u.EmptyLine()
}
