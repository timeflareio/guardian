package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"fmt"

	"github.com/pkg/errors"

	"github.com/spf13/cobra"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/guardian"
	"github.com/timeflareio/guardian/internal/monitoring"
	"go.uber.org/zap"
)

// NewStartCmd creates the start command
func NewStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the guardian service",
		Long: `Start the guardian service to begin monitoring for secret assignments and participating in the reveal protocol.

The service will:
1. Initialize connection to the blockchain
2. Verify guardian registration (must be registered first)
3. Start monitoring for secret assignments
4. Begin accepting and revealing shares automatically
5. Provide health check and metrics endpoints

The service runs continuously until stopped with Ctrl+C or a shutdown signal.
The guardian must be registered before starting the service.

Configuration comes from the file, GUARDIAN_* environment variables, and the
flags below, in that order of increasing precedence.`,
		Example: `  # Start guardian service (requires prior registration)
  guardiand start

  # Start with a custom configuration file
  guardiand --config-path /path/to/guardian.yaml start

  # Override configured values for this run
  guardiand start --log-level debug --rpc-endpoint http://node:26657

  # Register guardian first if not already registered
  guardianctl register && guardiand start`,
		RunE: runStart,
	}

	// Command-specific flags
	cmd.Flags().Int("startup-timeout", 30, "startup timeout in seconds")

	// Accepted and ignored: the service does not prompt, so there is nothing to
	// accept. The chain repository's devnet scripts pass this flag on every
	// guardian start, and an unknown-flag error there would be a cross-repository
	// break for a flag that costs nothing to tolerate. It goes once those
	// invocations have dropped it.
	cmd.Flags().Bool("accept", false, "")
	_ = cmd.Flags().MarkHidden("accept")

	// Runtime overrides. The registry supplies the flag name and the
	// documentation, so a configuration field becomes overridable by being
	// named here — see bindConfigFlags.
	bindConfigFlags(cmd,
		"chain_id", "rpc_endpoint", "grpc_endpoint",
		"log_level", "log_format",
		"bind_address", "metrics_port", "health_port", "dashboard_port",
		"polling_interval",
	)

	return cmd
}

func runStart(cmd *cobra.Command, args []string) error {
	u := printer(cmd)
	manager, cfg, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Report the configuration this run will use, then start. There is no
	// confirmation prompt: a service under a process supervisor has no terminal
	// to answer one at, so the prompt was not a safety feature but a startup
	// failure waiting for a host without a TTY — and because a declined prompt
	// returned nil, the failure exited zero.
	showStartConfig(u, cfg, manager.GetConfigPath())

	// Initialize logger
	logger, err := initLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return errors.Wrap(err, "failed to initialize logger")
	}
	defer func() { _ = logger.Sync() }() // Error ignored on defer

	u.Success("🎯 Starting Timeflare Guardian Service %s...", buildVersion())
	u.EmptyLine()

	logger.Info("Starting Timeflare Guardian Service",
		zap.String("version", buildVersion()),
		zap.String("chain_id", cfg.ChainID),
		zap.String("key_name", cfg.KeyName))

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Initialize guardian service
	guardianService, err := guardian.NewService(cfg, logger)
	if err != nil {
		logger.Error("Failed to initialize guardian service", zap.Error(err))
		return err
	}

	// Initialize monitoring and wire its metrics/health sinks into the
	// guardian service — this is what makes /metrics and /ready real.
	monitoringService := monitoring.NewService(cfg, logger)
	guardianService.SetObservability(monitoringService.Metrics(), monitoringService.Health())
	// The read-only operator dashboard: the daemon supplies the data, the
	// monitoring service owns the listener. Build info comes from here because
	// only the command knows the resolved config path and the binary's version.
	guardianService.SetBuildInfo(buildVersion(), manager.GetConfigPath())
	monitoringService.SetDashboardSource(guardianService)

	// Pre-flight: chain reachable, guardian registered, and the local share
	// key matches the registered record (startup self-check).
	if err := guardianService.VerifyRegistration(ctx); err != nil {
		logger.Error("Guardian pre-flight check failed",
			zap.Error(err),
			zap.String("error_detail", fmt.Sprintf("%+v", err)))
		u.EmptyLine()
		u.Error("Guardian Pre-flight Check Failed: %v", err)
		u.Text(ui.Indent1 + "If the guardian is not yet registered, run: ")
		u.Command("guardianctl register")
		u.EmptyLine()
		return errors.Wrap(err, "guardian pre-flight check failed")
	}

	// Start services
	errChan := make(chan error, 3)

	// Start monitoring service
	go func() {
		if err := monitoringService.Start(ctx); err != nil && err != context.Canceled {
			errChan <- errors.Wrap(err, "monitoring service failed")
		}
	}()

	// Start guardian service
	go func() {
		if err := guardianService.Start(ctx); err != nil && err != context.Canceled {
			errChan <- errors.Wrap(err, "guardian service failed")
		}
	}()

	u.Success("Guardian service started successfully!")
	u.Printf(ui.Indent1+"📊 Health endpoint:  http://localhost:%d/health\n", cfg.HealthPort)
	u.Printf(ui.Indent1+"📈 Metrics endpoint: http://localhost:%d/metrics\n", cfg.MetricsPort)
	u.EmptyLine()

	u.Note("Monitoring for secret assignments...")
	u.Note(ui.Indent1 + "Press Ctrl+C to shutdown gracefully")
	u.EmptyLine()

	logger.Info("Guardian service started successfully",
		zap.Int("health_port", cfg.HealthPort),
		zap.Int("metrics_port", cfg.MetricsPort))

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
		u.Warning("📡 Received shutdown signal: %s", sig.String())
	case err := <-errChan:
		logger.Error("Service error", zap.Error(err))
		u.Error("Service error: %v", err)
		return err
	}

	// Graceful shutdown
	u.Step("🔄 Shutting down guardian service...")
	logger.Info("Shutting down guardian service...")
	cancel()

	// Wait for services to shut down cleanly (grace period from config)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := guardian.GracefulShutdown(shutdownCtx, guardianService, monitoringService); err != nil {
		logger.Warn("Graceful shutdown failed", zap.Error(err))
		u.Warning("Graceful shutdown incomplete: %v", err)
	} else {
		u.Success("Guardian service stopped cleanly")
	}

	// In-memory hygiene: zero the cached share-decryption key on the way out
	// (key custody plan, Phase 1 — the cache lives for the process lifetime
	// by design; this bounds it at shutdown).
	cfg.WipeEncryptionKey()

	logger.Info("Guardian service stopped")
	return nil
}

func initLogger(level, format string) (*zap.Logger, error) {
	var config zap.Config

	// Configure based on format
	if format == "json" {
		config = zap.NewProductionConfig()
	} else {
		config = zap.NewDevelopmentConfig()
	}

	// Set log level
	switch level {
	case "debug":
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		config.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		config.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	}

	return config.Build()
}

// showStartConfig reports the configuration this run resolved to — file plus
// environment plus flags — so a container log records what the service actually
// started with rather than what its file says.
func showStartConfig(u *ui.Printer, config *config.Config, configPath string) {
	u.EmptyLine()
	u.Separator("🚀 Guardian Service Configuration")
	u.EmptyLine()

	// Network configuration
	u.Text(ui.Indent1)
	u.Key("%-18s", "Chain ID:")
	u.Value("%s\n", config.ChainID)

	u.Text(ui.Indent1)
	u.Key("%-18s", "RPC Endpoint:")
	u.Value("%s\n", config.RPCEndpoint)

	u.Text(ui.Indent1)
	u.Key("%-18s", "gRPC Endpoint:")
	u.Value("%s\n", config.GRPCEndpoint)

	u.EmptyLine()

	// Guardian identity
	u.Text(ui.Indent1)
	u.Key("%-18s", "Guardian Key:")
	u.Value("%s\n", config.KeyName)

	u.Text(ui.Indent1)
	u.Key("%-18s", "Keyring Backend:")
	u.Value("%s\n", config.KeyringBackend)

	u.EmptyLine()

	// Service configuration
	u.Text(ui.Indent1)
	u.Key("%-18s", "Log Level:")
	u.Value("%s\n", config.LogLevel)

	u.Text(ui.Indent1)
	u.Key("%-18s", "Log Format:")
	u.Value("%s\n", config.LogFormat)

	u.Text(ui.Indent1)
	u.Key("%-18s", "Health Port:")
	u.Value("%d\n", config.HealthPort)

	u.Text(ui.Indent1)
	u.Key("%-18s", "Metrics Port:")
	u.Value("%d\n", config.MetricsPort)

	u.EmptyLine()

	// Config file path
	u.Text(ui.Indent1)
	u.Key("%-18s", "Config File:")
	u.Path("%s\n", configPath)

	u.EmptyLine()
	u.Note("Starting guardian service with the above configuration.")
	u.EmptyLine()
}
