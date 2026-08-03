package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/guardian"
	"go.uber.org/zap"
)

// NewStatusCmd creates the status command
func NewStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show guardian status and health",
		Long: `Show the current status and health of the guardian.

This command will display:
- Guardian registration status
- Current balance and stake information
- Number of active secret assignments
- Recent activity summary
- Health check results
- Configuration summary

The status check connects to the blockchain to retrieve current information
and does not require the guardian service to be running.`,
		Example: `  # Show basic guardian status
  guardiand status

  # Show detailed status information
  guardiand status --detailed

  # Output status in JSON format
  guardiand status --format json

  # Check status with custom timeout
  guardiand status --timeout 60`,
		RunE: runStatus,
	}

	// Command-specific flags
	cmd.Flags().Bool("detailed", false, "show detailed status information")
	cmd.Flags().String("format", "text", "output format (text, json)")
	cmd.Flags().Int("timeout", 30, "status check timeout in seconds")

	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	// Get command flags
	detailed, _ := cmd.Flags().GetBool("detailed")
	format, _ := cmd.Flags().GetString("format")
	timeout, _ := cmd.Flags().GetInt("timeout")

	_, cfg, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Initialize logger (using config)
	logger, err := initLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return errors.Wrap(err, "failed to initialize logger")
	}
	defer func() { _ = logger.Sync() }() // Error ignored on defer

	logger.Debug("Getting guardian status",
		zap.String("chain_id", cfg.ChainID),
		zap.String("key_name", cfg.KeyName),
		zap.Bool("detailed", detailed))

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Initialize guardian service
	guardianService, err := guardian.NewService(cfg, logger)
	if err != nil {
		logger.Error("Failed to initialize guardian service", zap.Error(err))
		return err
	}

	// Get status information
	status, err := guardianService.GetStatus(ctx, detailed)
	if err != nil {
		logger.Error("Failed to get guardian status", zap.Error(err))
		return err
	}

	// Display status based on format
	switch format {
	case "json":
		return displayStatusJSON(u, status)
	case "text":
		return displayStatusText(u, status)
	default:
		return errors.Errorf("unsupported format: %s", format)
	}
}

func displayStatusText(u *ui.Printer, status *guardian.Status) error {
	u.Header("Guardian Status")
	u.Separator("===============")
	u.EmptyLine()

	// Basic information
	u.Printf("Guardian ID:      %s\n", status.GuardianID)
	u.Printf("Guardian Address: %s\n", status.Address)
	u.Printf("Chain ID:         %s\n", status.ChainID)
	u.Printf("Registration:     %s\n", formatRegistrationStatus(status.Registered))

	if status.Registered {
		// Guardian configuration from chain
		u.Printf("Stake Amount:     %s%s\n", status.StakeAmount, status.StakeDenom)
		u.Printf("Encryption Key:   %s\n", status.EncryptionPublicKey)
		u.Printf("Availability:     %s\n", formatAvailabilityRange(status.AvailableFrom, status.AvailableUntil, status.BlockHeight))
		u.Printf("Accepting Secrets:%s\n", formatAcceptingSecrets(status.AcceptingSecrets))
	}

	u.Printf("Balance:          %s\n", status.Balance)

	if status.BlockHeight == -1 {
		u.Printf("Block Height:     unavailable\n")
	} else {
		u.Printf("Block Height:     %d\n", status.BlockHeight)
	}

	u.Printf("Last Updated:     %s\n", status.LastUpdate.Format(time.RFC3339))

	// Activity summary
	u.TextLn("\nActivity Summary")
	u.TextLn("----------------")
	u.Printf("Active Secrets:   %d\n", status.ActiveSecrets)
	if status.Registered && status.LockedStake != "" {
		u.Printf("Locked Bonds:     %s%s\n", status.LockedStake, status.StakeDenom)
	}

	// Health status
	u.TextLn("\nHealth Status")
	u.TextLn("-------------")
	u.Printf("Overall Health:   %s\n", formatHealthStatus(status.Healthy))

	return nil
}

func displayStatusJSON(u *ui.Printer, status *guardian.Status) error {
	// Marshal the status struct to JSON with indentation
	jsonData, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal status to JSON")
	}

	u.TextLn(string(jsonData))
	return nil
}

func formatRegistrationStatus(registered bool) string {
	if registered {
		return "✅ Registered"
	}
	return "❌ Not Registered"
}

func formatHealthStatus(healthy bool) string {
	if healthy {
		return "✅ Healthy"
	}
	return "❌ Unhealthy"
}

func formatAvailabilityRange(availableFrom, availableUntil, currentBlock int64) string {
	if availableFrom == 0 && availableUntil == 0 {
		return "❌ Not configured"
	}

	status := ""

	// Determine current availability status
	if currentBlock >= availableFrom && currentBlock < availableUntil {
		status = "✅ Currently available"
	} else if currentBlock < availableFrom {
		status = "⏳ Will be available"
	} else {
		status = "❌ No longer available"
	}

	return fmt.Sprintf("%s (blocks %d-%d)", status, availableFrom, availableUntil)
}

func formatAcceptingSecrets(accepting bool) string {
	if accepting {
		return " ✅ Yes"
	}
	return " ❌ No"
}
