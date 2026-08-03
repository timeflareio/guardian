package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/monitoring"
)

// NewHealthCmd creates the health command
func NewHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check the health of a running guardian service",
		Long: `Check the health of a running guardian service via its health endpoint.

The command queries the health port from the guardian configuration and
exits non-zero if the service is unreachable or reports an unhealthy status,
making it suitable for scripts and process supervisors.`,
		Example: `  # Check the locally running guardian
  guardiand health

  # Check with a custom timeout
  guardiand health --timeout 5

  # Check a guardian on a different host
  guardiand health --url http://guardian-host:21000`,
		RunE: runHealth,
	}

	cmd.Flags().String("url", "", "health server base URL (defaults to http://localhost:<health-port>)")
	cmd.Flags().Int("timeout", 10, "health check timeout in seconds")

	return cmd
}

func runHealth(cmd *cobra.Command, args []string) error {
	url, _ := cmd.Flags().GetString("url")
	timeout, _ := cmd.Flags().GetInt("timeout")

	if url == "" {
		url = fmt.Sprintf("http://localhost:%d", cfg.HealthPort)
	}

	status, err := monitoring.CheckHealth(cmd.Context(), url, time.Duration(timeout)*time.Second)
	if err != nil {
		printError("Guardian is unhealthy: %v\n", err)
		return err
	}

	printSuccess("Guardian is %s (checked %s/health at %s)\n", status.Status, url, status.Timestamp)
	return nil
}
