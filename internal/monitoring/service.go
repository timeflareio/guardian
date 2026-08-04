package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/dashboard"
	"go.uber.org/zap"
)

// Service provides health checks and metrics
type Service struct {
	config       *config.Config
	logger       *zap.Logger
	registry     *prometheus.Registry
	metrics      *Metrics
	health       *Health
	httpServer   *http.Server
	healthServer *http.Server
	// dashboardServer is nil unless a Source was supplied AND the config
	// enables it: a dashboard with nothing to show would be worse than none.
	dashboardServer *http.Server
	dashboardSource dashboard.Source
}

// Metrics holds all Prometheus metrics
type Metrics struct {
	// Guardian operation metrics
	SecretsProcessed    prometheus.Counter
	SuccessfulReveals   prometheus.Counter
	FailedReveals       prometheus.Counter
	AssignmentsAccepted prometheus.Counter
	AssignmentsRejected prometheus.Counter
	WindowsMissed       prometheus.Counter

	// Performance metrics
	ProcessingLatency prometheus.Histogram
	BlockchainLatency prometheus.Histogram
	RevealTiming      prometheus.Histogram

	// System metrics
	GuardianBalance prometheus.Gauge
	ActiveSecrets   prometheus.Gauge
	LastBlockHeight prometheus.Gauge

	// Error metrics
	ErrorCount          prometheus.Counter
	ValidationFailures  *prometheus.CounterVec
	TransactionFailures *prometheus.CounterVec
}

// NewService creates a new monitoring service. The returned service's
// Metrics() and Health() are handed to the guardian service, which records
// into them — that wiring is what makes /metrics and /health real.
func NewService(cfg *config.Config, logger *zap.Logger) *Service {
	registry := prometheus.NewRegistry()
	metrics := createMetrics(registry)

	// Readiness staleness bound: several polling intervals, floored so slow
	// devnets with long block times don't flap.
	maxStale := max(3*cfg.PollingInterval, 30*time.Second)

	return &Service{
		config:   cfg,
		logger:   logger,
		registry: registry,
		metrics:  metrics,
		health:   NewHealth(maxStale),
	}
}

// createMetrics creates and registers all Prometheus metrics
func createMetrics(registry *prometheus.Registry) *Metrics {
	metrics := &Metrics{
		// Guardian operation metrics
		SecretsProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_secrets_processed_total",
			Help: "Total number of monitoring cycles processed",
		}),
		SuccessfulReveals: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_successful_reveals_total",
			Help: "Total number of successful share reveals",
		}),
		FailedReveals: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_failed_reveals_total",
			Help: "Total number of failed share reveals",
		}),
		AssignmentsAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_assignments_accepted_total",
			Help: "Total number of assignments accepted",
		}),
		AssignmentsRejected: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_assignments_rejected_total",
			Help: "Total number of assignments rejected",
		}),
		WindowsMissed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_reveal_windows_missed_total",
			Help: "Reveal windows that closed without our reveal — leading indicator of slashing",
		}),

		// Performance metrics
		ProcessingLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "guardian_processing_duration_seconds",
			Help:    "Time spent processing secrets",
			Buckets: prometheus.DefBuckets,
		}),
		BlockchainLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "guardian_blockchain_request_duration_seconds",
			Help:    "Time spent on blockchain requests",
			Buckets: prometheus.DefBuckets,
		}),
		RevealTiming: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "guardian_reveal_timing_seconds",
			Help:    "Time from reveal window start to actual reveal",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		}),

		// System metrics
		GuardianBalance: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_balance",
			Help: "Guardian account balance in the base denom",
		}),
		ActiveSecrets: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_active_secrets",
			Help: "Number of active secret assignments",
		}),
		LastBlockHeight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_last_block_height",
			Help: "Last observed block height",
		}),

		// Error metrics
		ErrorCount: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_errors_total",
			Help: "Total number of errors encountered",
		}),
		ValidationFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "guardian_validation_failures_total",
				Help: "Total number of validation failures",
			},
			[]string{"type"}, // hmac, structure, etc.
		),
		TransactionFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "guardian_transaction_failures_total",
				Help: "Total number of transaction failures",
			},
			[]string{"operation"}, // register, confirm, reveal
		),
	}

	// Register all metrics
	registry.MustRegister(
		metrics.SecretsProcessed,
		metrics.SuccessfulReveals,
		metrics.FailedReveals,
		metrics.AssignmentsAccepted,
		metrics.AssignmentsRejected,
		metrics.WindowsMissed,
		metrics.ProcessingLatency,
		metrics.BlockchainLatency,
		metrics.RevealTiming,
		metrics.GuardianBalance,
		metrics.ActiveSecrets,
		metrics.LastBlockHeight,
		metrics.ErrorCount,
		metrics.ValidationFailures,
		metrics.TransactionFailures,
	)

	return metrics
}

// SetDashboardSource supplies the data the operator dashboard renders. Wired
// by the start command, mirroring SetObservability — the daemon implements the
// interface, this service mounts the handlers, and neither package imports the
// other's owner.
//
// Must be called before Start: the listener is only bound when a source exists.
func (s *Service) SetDashboardSource(src dashboard.Source) {
	s.dashboardSource = src
}

// dashboardAuthenticator chooses the dashboard's access model from the
// effective config.
//
// A configured credential is always honoured, including on loopback: the
// loopback exemption is permission to serve without one, not a rule that
// discards one the operator has set. Start never reaches the unauthenticated
// branch on an exposed bind address — DashboardWithheld withholds the listener
// first — so this cannot serve an exposed page without a credential.
func (s *Service) dashboardAuthenticator() dashboard.Authenticator {
	if s.config.DashboardPasswordHash == "" {
		return dashboard.NoAuth()
	}
	realm := "timeflare guardian"
	if identity := s.config.DashboardIdentity(); identity != "" {
		realm += " " + identity
	}
	return dashboard.BasicAuth(realm, s.config.DashboardPasswordHash, s.logger)
}

// Metrics returns the metrics sink for the guardian service to record into.
func (s *Service) Metrics() *Metrics {
	return s.metrics
}

// Health returns the shared health tracker for the guardian service to
// record into.
func (s *Service) Health() *Health {
	return s.health
}

// GetMetrics is retained as an alias of Metrics for existing callers.
func (s *Service) GetMetrics() *Metrics {
	return s.metrics
}

// Start binds and serves the metrics and health listeners. Binding happens
// synchronously so a taken port fails startup loudly instead of leaving the
// guardian running blind with neither health nor metrics.
func (s *Service) Start(ctx context.Context) error {
	// Metrics server (health endpoints kept here too for scraper convenience)
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/health", s.handleHealth)
	metricsMux.HandleFunc("/ready", s.handleReady)
	metricsMux.Handle("/metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))

	metricsAddr := fmt.Sprintf("%s:%d", s.config.BindAddress, s.config.MetricsPort)
	metricsLn, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		return fmt.Errorf("metrics listener failed to bind %s: %w", metricsAddr, err)
	}
	s.httpServer = &http.Server{Addr: metricsAddr, Handler: metricsMux}

	// Dedicated health server on the configured health port
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", s.handleHealth)
	healthMux.HandleFunc("/ready", s.handleReady)

	healthAddr := fmt.Sprintf("%s:%d", s.config.BindAddress, s.config.HealthPort)
	healthLn, err := net.Listen("tcp", healthAddr)
	if err != nil {
		_ = metricsLn.Close()
		return fmt.Errorf("health listener failed to bind %s: %w", healthAddr, err)
	}
	s.healthServer = &http.Server{Addr: healthAddr, Handler: healthMux}

	// Dashboard listener: the shared bind address, alongside health and metrics.
	// Bound synchronously like the others so a taken port fails startup rather
	// than leaving the operator with a silently missing dashboard.
	//
	// Fail closed, but do not take the guardian offline: an exposed dashboard
	// with no credential is not bound at all, while health, metrics and — the
	// point — reveals carry on. A missed reveal window is slashable, so failing
	// a guardian's economic function over a dashboard misconfiguration would
	// cost the operator real amounts to protect a page.
	var dashboardLn net.Listener
	switch {
	case !s.config.EnableDashboard || s.dashboardSource == nil:
		// Not enabled, or nothing to serve from yet.
	case s.config.DashboardWithheld():
		s.logger.Error("Operator dashboard NOT served: it would be reachable beyond loopback without a credential — set one with 'guardiand config set-dashboard-password' (the guardian is otherwise unaffected and continues to reveal)",
			zap.String("bind_address", s.config.BindAddress),
			zap.Int("dashboard_port", s.config.DashboardPort))
	default:
		dashboardAddr := fmt.Sprintf("%s:%d", s.config.BindAddress, s.config.DashboardPort)
		dashboardLn, err = net.Listen("tcp", dashboardAddr)
		if err != nil {
			_ = metricsLn.Close()
			_ = healthLn.Close()
			return fmt.Errorf("dashboard listener failed to bind %s: %w", dashboardAddr, err)
		}
		s.dashboardServer = &http.Server{
			Addr:    dashboardAddr,
			Handler: dashboard.Handler(s.dashboardSource, s.dashboardAuthenticator()),
			// The dashboard serves a browser rather than a scraper, so it gets
			// read/write deadlines the other listeners do not need — a hung
			// browser connection must not pin a goroutine indefinitely.
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
		}
	}

	fields := []zap.Field{
		zap.String("bind_address", s.config.BindAddress),
		zap.Int("metrics_port", s.config.MetricsPort),
		zap.Int("health_port", s.config.HealthPort),
	}
	if dashboardLn != nil {
		fields = append(fields, zap.Int("dashboard_port", s.config.DashboardPort))
		// What the operator surface is doing is a thing to know at startup, not
		// to infer from a port table. The one state that warns is the one with a
		// residual risk: a credential crossing a routable network in base64.
		switch {
		case !s.config.DashboardAuthRequired():
			s.logger.Info("Operator dashboard served on loopback without a credential",
				zap.String("bind_address", s.config.BindAddress),
				zap.Int("dashboard_port", s.config.DashboardPort))
		case s.config.DashboardTLSEnabled():
			s.logger.Info("Operator dashboard served with authentication over TLS",
				zap.String("bind_address", s.config.BindAddress),
				zap.Int("dashboard_port", s.config.DashboardPort))
		default:
			s.logger.Warn("Operator dashboard is authenticated but NOT encrypted — Basic auth defends against unauthorised readers, not against a network eavesdropper; set dashboard_tls_cert_file and dashboard_tls_key_file, or front it with a TLS proxy",
				zap.String("bind_address", s.config.BindAddress),
				zap.Int("dashboard_port", s.config.DashboardPort))
		}
	}
	s.logger.Info("Starting monitoring service", fields...)

	serveErr := make(chan error, 3)
	go func() {
		if err := s.httpServer.Serve(metricsLn); err != nil && err != http.ErrServerClosed {
			serveErr <- fmt.Errorf("metrics server failed: %w", err)
		}
	}()
	go func() {
		if err := s.healthServer.Serve(healthLn); err != nil && err != http.ErrServerClosed {
			serveErr <- fmt.Errorf("health server failed: %w", err)
		}
	}()
	if dashboardLn != nil {
		go func() {
			var err error
			if s.config.DashboardTLSEnabled() {
				// In-process TLS rather than "put a proxy in front of it": the
				// Docker-only operator this dashboard is designed around may
				// have no proxy, and telling them to acquire one is how a
				// default stays insecure. Certificate lifecycle stays theirs.
				err = s.dashboardServer.ServeTLS(dashboardLn, s.config.DashboardTLSCertFile, s.config.DashboardTLSKeyFile)
			} else {
				err = s.dashboardServer.Serve(dashboardLn)
			}
			if err != nil && err != http.ErrServerClosed {
				serveErr <- fmt.Errorf("dashboard server failed: %w", err)
			}
		}()
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		// Shutdown driven by the caller's grace period (shutdown_timeout via
		// the start command), not a hardcoded local deadline.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		return s.shutdown(shutdownCtx)
	}
}

// Stop stops the monitoring service, honouring the caller's context for the
// shutdown grace period (implements Shutdownable).
func (s *Service) Stop(ctx context.Context) error {
	return s.shutdown(ctx)
}

// shutdown gracefully stops both HTTP servers within ctx's deadline.
func (s *Service) shutdown(ctx context.Context) error {
	var shutdownErr error
	for _, server := range []*http.Server{s.httpServer, s.healthServer, s.dashboardServer} {
		if server == nil {
			continue
		}
		if err := server.Shutdown(ctx); err != nil {
			s.logger.Error("Failed to shutdown monitoring server", zap.Error(err))
			shutdownErr = err
		}
	}
	if shutdownErr != nil {
		return shutdownErr
	}

	s.logger.Info("Monitoring service stopped")
	return nil
}

// handleHealth reports liveness: the process's dependencies work (chain
// reachable, decryption key loads). Unhealthy → HTTP 503.
func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := s.health.Snapshot()

	status := "healthy"
	code := http.StatusOK
	if !snap.Healthy {
		status = "unhealthy"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Status    string `json:"status"`
		Timestamp string `json:"timestamp"`
		Snapshot
	}{Status: status, Timestamp: time.Now().Format(time.RFC3339), Snapshot: snap})
}

// handleReady reports readiness: healthy AND registered AND the monitoring
// loop showed activity recently. A wedged loop or dead RPC reports unready so
// supervisors restart the guardian.
func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	snap := s.health.Snapshot()

	if !snap.Ready {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(snap)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}
