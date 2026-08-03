package cli

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
)

func NewUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update existing guardian registration",
		Long: `Update an existing guardian registration with new parameters.

The update transaction is built, signed with your keyring key, and broadcast
in-process — the exact parameters are shown and confirmed before anything is
sent.

All parameters are optional - only specify the ones you want to change:
- available-from: Blocks from current when guardian becomes available (0 = preserve existing)
- available-until: Blocks from current when guardian stops being available
- stake: Additional float deposit to add (e.g., 5000000000uveil)
- accepting-secrets: Whether to accept new secret assignments

Note: Encryption keys are not updated here — each epoch's key binding is
permanently immutable; use 'guardiand rotate-key' to rotate forward for
future assignments.`,
		Example: `  # Update availability window
  guardiand update --available-until 28800

  # Add more float
  guardiand update --stake 5000000000uveil

  # Stop accepting new secrets temporarily
  guardiand update --accepting-secrets=false

  # Update multiple parameters at once
  guardiand update --available-until 14400 --accepting-secrets=true`,
		RunE:         runUpdate,
		SilenceUsage: true, // Don't show usage on errors
	}

	// All parameters are optional for updates
	cmd.Flags().Int64("available-from", 0, "Blocks from current when guardian becomes available (0 = preserve existing)")
	cmd.Flags().Int64("available-until", 0, "Blocks from current when guardian stops being available")
	cmd.Flags().String("stake", "", "Float deposit increment (e.g., 5000000000uveil)")
	cmd.Flags().Bool("accepting-secrets", true, "Whether to accept new secrets")
	cmd.Flags().Bool("accept", false, "automatically accept and execute without prompting")

	return cmd
}

func runUpdate(cmd *cobra.Command, args []string) error {
	_, cfg, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Get flag values
	availableFrom, _ := cmd.Flags().GetInt64("available-from")
	availableUntil, _ := cmd.Flags().GetInt64("available-until")
	stakeAmount, _ := cmd.Flags().GetString("stake")
	acceptingSecrets, _ := cmd.Flags().GetBool("accepting-secrets")
	autoAccept, _ := cmd.Flags().GetBool("accept")

	// Check which flags were explicitly set
	availableFromFlag := cmd.Flags().Changed("available-from")
	availableUntilFlag := cmd.Flags().Changed("available-until")
	stakeFlag := cmd.Flags().Changed("stake")
	acceptingSecretsFlag := cmd.Flags().Changed("accepting-secrets")

	// Validate at least one update parameter is provided
	if !availableFromFlag && !availableUntilFlag && !stakeFlag && !acceptingSecretsFlag {
		return errors.New("no update parameters provided — see 'guardiand update --help'")
	}

	opts := chain.GuardianUpdateOptions{}
	if availableFromFlag {
		opts.AvailableFrom = availableFrom
	}
	if availableUntilFlag {
		opts.AvailableUntil = availableUntil
	}
	if stakeFlag {
		deposit, err := sdk.ParseCoinNormalized(stakeAmount)
		if err != nil {
			return errors.Wrapf(err, "invalid stake amount %q", stakeAmount)
		}
		opts.Deposit = &deposit
	}
	if acceptingSecretsFlag {
		opts.AcceptingSecrets = &acceptingSecrets
	}

	// Show update and get confirmation (unless auto-accept is enabled)
	if !autoAccept && !showUpdateAndConfirm(cfg, stakeAmount, availableFrom, availableUntil, acceptingSecrets, availableFromFlag, availableUntilFlag, stakeFlag, acceptingSecretsFlag) {
		printNote("Update cancelled.")
		return nil
	}

	logger, err := initLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return errors.Wrap(err, "failed to initialize logger")
	}
	defer func() { _ = logger.Sync() }()

	client, err := chain.NewClient(cfg, logger)
	if err != nil {
		return errors.Wrap(err, "failed to initialise chain client")
	}
	defer func() { _ = client.Close() }()

	txHash, err := client.GuardianUpdate(cmd.Context(), opts)
	if err != nil {
		return errors.Wrap(err, "update failed")
	}

	showUpdateSuccess(cfg, txHash)
	return nil
}

func showUpdateAndConfirm(cfg *config.Config, stakeAmount string, availableFrom, availableUntil int64, acceptingSecrets bool, availableFromFlag, availableUntilFlag, stakeFlag, acceptingSecretsFlag bool) bool {
	headerColor := color.New(color.FgGreen, color.Bold)
	sectionColor := color.New(color.FgYellow, color.Bold)
	valueColor := color.New(color.FgCyan)
	updateColor := color.New(color.FgYellow)

	printEmptyLine()
	headerColor.Println("🔄 Guardian Registration Update Preview")
	headerColor.Println("═══════════════════════════════════════")
	printEmptyLine()

	sectionColor.Println("📋 Update Details:")
	printEmptyLine()

	printText("   • Guardian Key:        ")
	valueColor.Printf("%s\n", cfg.KeyName)

	if availableFromFlag {
		printText("   • Available From:      ")
		if availableFrom == 0 {
			valueColor.Print("Current block + 1")
		} else {
			valueColor.Printf("Current block + %d", availableFrom)
		}
		updateColor.Printf(" (UPDATED)\n")
	}

	if availableUntilFlag {
		printText("   • Available Until:     ")
		valueColor.Printf("Current block + %d", availableUntil)
		updateColor.Printf(" (UPDATED)\n")
	}

	if stakeFlag {
		printText("   • Float Addition:      ")
		valueColor.Print(stakeAmount)
		updateColor.Printf(" (UPDATED)\n")
	}

	if acceptingSecretsFlag {
		printText("   • Accepting Secrets:   ")
		if acceptingSecrets {
			valueColor.Print("Yes")
		} else {
			valueColor.Print("No")
		}
		updateColor.Printf(" (UPDATED)\n")
	}

	printEmptyLine()
	sectionColor.Println("⚠️  This will:")
	printTextLn("   • Sign MsgGuardianUpdate with your keyring key and broadcast it")
	printTextLn("   • Apply only the parameters marked UPDATED — others remain unchanged")
	printEmptyLine()

	return promptForConfirmation("Execute this update?")
}

func showUpdateSuccess(cfg *config.Config, txHash string) {
	printEmptyLine()
	printTextLn("✅ Guardian Update Broadcast Successfully!")
	printTextLn("═══════════════════════════════════════")
	printEmptyLine()
	printf("   • Transaction: %s\n", txHash)
	printf("   • Chain ID:    %s\n", cfg.ChainID)
	printEmptyLine()
	printTextLn("📋 Next Steps:")
	printTextLn("   • Use 'guardiand status' to verify your updated registration")
	printEmptyLine()
}
