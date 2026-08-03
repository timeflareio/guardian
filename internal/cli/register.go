package cli

import (
	"strconv"

	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/guardian"
)

// NewRegisterCmd creates the register command
func NewRegisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register guardian with the network (new registrations only)",
		Long: `Register a new guardian with the Timeflare network.

The registration transaction is built, signed with your keyring key, and
broadcast in-process — the exact parameters are shown and confirmed before
anything is sent.

IMPORTANT: This command only handles NEW registrations. Use 'guardiand update'
for changes to an existing registration.

Registration charges the protocol entry fee (1,000 VEIL, routed to validators) in
addition to the float deposit. The deposit is your working capital: a bond
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
  guardiand register

  # Explicit deposit and availability window
  guardiand register --stake-amount 10000000000uveil --available-until 14400

  # Registration NOT accepting secrets initially
  guardiand register --stake-amount 15000000000uveil --available-until 28800 --accepting-secrets=false`,
		RunE:         runRegister,
		SilenceUsage: true, // Don't show usage on errors
	}

	// Command-specific flags (all optional — config defaults apply)
	cmd.Flags().String("stake-amount", "", "initial float deposit (default: configured stake_amount; the 1,000 VEIL entry fee is charged separately and paid to validators)")
	cmd.Flags().Int64("available-from", 0, "blocks from current when guardian becomes available (default: 0 = immediate)")
	cmd.Flags().String("available-until", "", "blocks from current when guardian stops being available (default: chain maximum)")
	cmd.Flags().Bool("accepting-secrets", true, "whether guardian accepts new secret assignments (default: true)")
	cmd.Flags().Bool("accept", false, "automatically accept and execute without prompting")

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
		return errors.New("encryption public key is required. Set it in config with 'guardiand config set encryption-public-key <key>'")
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
		u.Command("guardiand wallet create\n")
		u.Text(ui.Indent1 + "Or restore it with: ")
		u.Command("guardiand wallet import-from-mnemonic\n")
		u.EmptyLine()
		return errors.New("guardian key not found")
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

	service, err := guardian.NewService(cfg, logger)
	if err != nil {
		return errors.Wrap(err, "failed to initialise guardian service")
	}

	if err := service.RegisterWithOptions(cmd.Context(), &guardian.RegistrationOptions{
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

// showRegistrationAndConfirm displays the registration parameters and asks
// for confirmation before the transaction is signed and broadcast.
func showRegistrationAndConfirm(u *ui.Printer, cfg *config.Config, guardianAddress, stakeAmount string, availableFrom, availableUntil int64, acceptingSecrets bool) bool {
	blockSeconds := cfg.BlockTime.Seconds()

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
	u.Value("~%d blocks (~%.1f hours)\n", availableUntil-availableFrom, float64(availableUntil-availableFrom)*blockSeconds/3600.0)

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
	blockSeconds := cfg.BlockTime.Seconds()

	u.EmptyLine()
	headerColor.Println("✅ Guardian Registration Successful!")
	headerColor.Println("═══════════════════════════════════")
	u.EmptyLine()

	sectionColor.Println("📅 Availability Window:")
	u.EmptyLine()
	u.Text("   • Status:     ")
	valueColor.Println("Registered and ready for assignments")
	u.Text("   • Duration:   ")
	valueColor.Printf("%d blocks (~%.1f hours)\n", availableUntil-availableFrom, float64(availableUntil-availableFrom)*blockSeconds/3600.0)
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
		u.TextLn("   • To become eligible, run 'guardiand update --accepting-secrets=true'")
	}
	u.Printf("   • Monitor your guardian's health at http://localhost:%d/health\n", cfg.HealthPort)
	u.EmptyLine()
}
