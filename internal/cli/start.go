package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
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
  guardiand register && guardiand start`,
		RunE: runStart,
	}

	// Command-specific flags
	cmd.Flags().Int("startup-timeout", 30, "startup timeout in seconds")

	// Accepted and ignored. The service no longer prompts, so there is nothing
	// to accept — but the chain repository's devnet scripts pass this flag on
	// every guardian start, and an unknown-flag error there would be a
	// cross-repository break for a flag that costs nothing to tolerate. It is
	// removed once those invocations have dropped it.
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
	manager, cfg, err := requireConfig(cmd)
	if err != nil {
		return err
	}

	// Report the configuration this run will use, then start. There is no
	// confirmation prompt: a service under a process supervisor has no terminal
	// to answer one at, so the prompt was not a safety feature but a startup
	// failure waiting for a host without a TTY — and because a declined prompt
	// returned nil, the failure exited zero.
	showStartConfig(cfg, manager.GetConfigPath())

	// Initialize logger
	logger, err := initLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return errors.Wrap(err, "failed to initialize logger")
	}
	defer func() { _ = logger.Sync() }() // Error ignored on defer

	printSuccess("🎯 Starting Timeflare Guardian Service %s...", buildVersion())
	printEmptyLine()

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
			zap.String("error_detail", sprintf("%+v", err)))
		printEmptyLine()
		printError("Guardian Pre-flight Check Failed: %v", err)
		printText(indent1 + "If the guardian is not yet registered, run: ")
		printCommand("guardiand register")
		printEmptyLine()
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

	printSuccess("Guardian service started successfully!")
	printf(indent1+"📊 Health endpoint:  http://localhost:%d/health\n", cfg.HealthPort)
	printf(indent1+"📈 Metrics endpoint: http://localhost:%d/metrics\n", cfg.MetricsPort)
	printEmptyLine()

	printNote("Monitoring for secret assignments...")
	printNote(indent1 + "Press Ctrl+C to shutdown gracefully")
	printEmptyLine()

	logger.Info("Guardian service started successfully",
		zap.Int("health_port", cfg.HealthPort),
		zap.Int("metrics_port", cfg.MetricsPort))

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
		printWarning("📡 Received shutdown signal: %s", sig.String())
	case err := <-errChan:
		logger.Error("Service error", zap.Error(err))
		printError("Service error: %v", err)
		return err
	}

	// Graceful shutdown
	printStep("🔄 Shutting down guardian service...")
	logger.Info("Shutting down guardian service...")
	cancel()

	// Wait for services to shut down cleanly (grace period from config)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := guardian.GracefulShutdown(shutdownCtx, guardianService, monitoringService); err != nil {
		logger.Warn("Graceful shutdown failed", zap.Error(err))
		printWarning("Graceful shutdown incomplete: %v", err)
	} else {
		printSuccess("Guardian service stopped cleanly")
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
func showStartConfig(config *config.Config, configPath string) {
	printEmptyLine()
	printSeparator("🚀 Guardian Service Configuration")
	printEmptyLine()

	// Network configuration
	printText(indent1)
	printKey("%-18s", "Chain ID:")
	printValue("%s\n", config.ChainID)

	printText(indent1)
	printKey("%-18s", "RPC Endpoint:")
	printValue("%s\n", config.RPCEndpoint)

	printText(indent1)
	printKey("%-18s", "gRPC Endpoint:")
	printValue("%s\n", config.GRPCEndpoint)

	printEmptyLine()

	// Guardian identity
	printText(indent1)
	printKey("%-18s", "Guardian Key:")
	printValue("%s\n", config.KeyName)

	printText(indent1)
	printKey("%-18s", "Keyring Backend:")
	printValue("%s\n", config.KeyringBackend)

	printEmptyLine()

	// Service configuration
	printText(indent1)
	printKey("%-18s", "Log Level:")
	printValue("%s\n", config.LogLevel)

	printText(indent1)
	printKey("%-18s", "Log Format:")
	printValue("%s\n", config.LogFormat)

	printText(indent1)
	printKey("%-18s", "Health Port:")
	printValue("%d\n", config.HealthPort)

	printText(indent1)
	printKey("%-18s", "Metrics Port:")
	printValue("%d\n", config.MetricsPort)

	printEmptyLine()

	// Config file path
	printText(indent1)
	printKey("%-18s", "Config File:")
	printPath("%s\n", configPath)

	printEmptyLine()
	printNote("Starting guardian service with the above configuration.")
	printEmptyLine()
}
