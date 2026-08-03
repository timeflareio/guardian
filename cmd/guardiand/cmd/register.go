package cmd

import (
	"os"
	"strconv"

	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	secretstypes "github.com/timeflareio/chain/x/secrets/types"
	"github.com/timeflareio/guardian/guardian"
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
	// Check if config exists first (before parameter validation)
	configPath := cfgManager.GetConfigPath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		ShowNoConfigMessage(configPath)
		return nil
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
		printEmptyLine()
		printError("Guardian key not accessible: %v", err)
		printText(indent1 + "Create it with: ")
		printCommand("guardiand wallet create\n")
		printText(indent1 + "Or restore it with: ")
		printCommand("guardiand wallet import-from-mnemonic\n")
		printEmptyLine()
		return errors.New("guardian key not found")
	}

	// Show the registration and either execute or bail
	if !autoAccept && !showRegistrationAndConfirm(guardianAddress, stakeAmount, availableFrom, availableUntil, acceptingSecrets) {
		printNote("Registration cancelled.")
		printEmptyLine()
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

	showRegistrationSuccess(availableFrom, availableUntil, acceptingSecrets)
	return nil
}

// showRegistrationAndConfirm displays the registration parameters and asks
// for confirmation before the transaction is signed and broadcast.
func showRegistrationAndConfirm(guardianAddress, stakeAmount string, availableFrom, availableUntil int64, acceptingSecrets bool) bool {
	blockSeconds := cfg.BlockTime.Seconds()

	printEmptyLine()
	printSeparator("🚀 Guardian Registration Preview")
	printEmptyLine()

	printHeader("📋 Registration Details:")
	printEmptyLine()

	printText(indent1 + "• Guardian Key:        ")
	printValue("%s\n", cfg.KeyName)

	printText(indent1 + "• Guardian Address:    ")
	printValue("%s\n", guardianAddress)

	printText(indent1 + "• Float Deposit:       ")
	printValue("%s\n", stakeAmount)

	printText(indent1 + "• Available From:      ")
	if availableFrom > 0 {
		printValue("Current block + %d\n", availableFrom)
	} else {
		printValue("Current block + 1\n")
	}

	printText(indent1 + "• Available Until:     ")
	printValue("Current block + %d\n", availableUntil)

	printText(indent1 + "• Duration:            ")
	printValue("~%d blocks (~%.1f hours)\n", availableUntil-availableFrom, float64(availableUntil-availableFrom)*blockSeconds/3600.0)

	printText(indent1 + "• Accepting Secrets:   ")
	if acceptingSecrets {
		printValue("Yes (accepting new assignments)\n")
	} else {
		printNote("No (not accepting new assignments)\n")
	}

	printEmptyLine()
	printHeader("⚠️  This will:")
	printTextLn(indent1 + "• Sign MsgGuardianRegister with your keyring key and broadcast it")
	printTextLn(indent1 + "• Burn the 1,000 VEIL entry fee and deposit the specified float from your account")
	printTextLn(indent1 + "• Make your guardian available for the specified duration")
	printTextLn(indent1 + "• You must run 'guardiand start' to begin actively handling assignments")
	printEmptyLine()

	return promptForConfirmation("Execute this registration?")
}

// showRegistrationSuccess displays success message and next steps
func showRegistrationSuccess(availableFrom, availableUntil int64, acceptingSecrets bool) {
	headerColor := color.New(color.FgGreen, color.Bold)
	sectionColor := color.New(color.FgYellow, color.Bold)
	commandColor := color.New(color.FgGreen, color.Bold)
	valueColor := color.New(color.FgCyan)
	blockSeconds := cfg.BlockTime.Seconds()

	printEmptyLine()
	headerColor.Println("✅ Guardian Registration Successful!")
	headerColor.Println("═══════════════════════════════════")
	printEmptyLine()

	sectionColor.Println("📅 Availability Window:")
	printEmptyLine()
	printText("   • Status:     ")
	valueColor.Println("Registered and ready for assignments")
	printText("   • Duration:   ")
	valueColor.Printf("%d blocks (~%.1f hours)\n", availableUntil-availableFrom, float64(availableUntil-availableFrom)*blockSeconds/3600.0)
	printEmptyLine()

	sectionColor.Println("🚀 Next Steps:")
	printEmptyLine()
	printTextLn("   Start your guardian service to begin accepting secret assignments:")
	printText("   ")
	commandColor.Println("guardiand start")
	printEmptyLine()

	sectionColor.Println("⚠️  Important Reminders:")
	printEmptyLine()
	if acceptingSecrets {
		printTextLn("   • Your guardian is ELIGIBLE to receive new secret assignments")
		printTextLn("   • Run 'guardiand start' to begin actively handling assignments")
		printTextLn("   • Missing reveals while assigned will result in slashing penalties")
	} else {
		printTextLn("   • Your guardian is NOT eligible to receive new secret assignments")
		printTextLn("   • To become eligible, run 'guardiand update --accepting-secrets=true'")
	}
	printf("   • Monitor your guardian's health at http://localhost:%d/health\n", cfg.HealthPort)
	printEmptyLine()
}
