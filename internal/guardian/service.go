package guardian

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/monitoring"
	"go.uber.org/zap"
)

// Service is the main guardian service
type Service struct {
	config *config.Config
	logger *zap.Logger
	client chain.ClientInterface

	// Guardian state
	isRegistered bool
	isRunning    bool

	// Service components
	registrationManager *RegistrationManager
	shareRevealService  *ShareRevealService
	eventMonitor        *EventMonitor

	// Secret tracking with active cache system; refreshed by event resyncs
	// (primary) and the fallback polling loop
	activeSecretCache *ActiveSecretCache

	// Observability sinks (nil-safe; wired by the start command)
	metrics *monitoring.Metrics
	health  *monitoring.Health

	// Dashboard-facing runtime facts (dashboard plan §1 panel 1). startedAt is
	// what makes "since process start" a statement rather than an assumption,
	// and it is stamped at construction so uptime is honest even for a service
	// that never reached Start.
	startedAt    time.Time
	version      string
	configPath   string
	observations *Observations

	// lastKnownHeight lets a tick degrade gracefully when the height query
	// fails: confirmations still run against the last observed height.
	lastKnownHeight atomic.Int64

	// Internal state. Guards the flags only — never held across blocking
	// work (the old code held it for the daemon's lifetime, deadlocking any
	// concurrent GetStatus/Stop).
	mu sync.RWMutex
}

// Status represents the guardian's current status
type Status struct {
	GuardianID string `json:"guardian_id"`
	Address    string `json:"address"`
	ChainID    string `json:"chain_id"`
	Registered bool   `json:"registered"`
	Available  bool   `json:"available"`

	// Guardian chain information
	EncryptionPublicKey string `json:"encryption_public_key"`
	AvailableFrom       int64  `json:"available_from"`
	AvailableUntil      int64  `json:"available_until"`
	StakeAmount         string `json:"stake_amount"`
	StakeDenom          string `json:"stake_denom"`
	LockedStake         string `json:"locked_stake"`
	AcceptingSecrets    bool   `json:"accepting_secrets"`

	// Service status
	Balance       string    `json:"balance"`
	BlockHeight   int64     `json:"block_height"`
	LastUpdate    time.Time `json:"last_update"`
	ActiveSecrets int       `json:"active_secrets"`
	Healthy       bool      `json:"healthy"`
}

// NewService creates a new guardian service with a native chain client.
func NewService(cfg *config.Config, logger *zap.Logger) (*Service, error) {
	client, err := chain.NewClient(cfg, logger)
	if err != nil {
		return nil, err
	}
	return NewServiceWithClient(cfg, client, logger), nil
}

// NewServiceWithClient creates a guardian service over an existing client
// (tests inject mocks here).
func NewServiceWithClient(cfg *config.Config, client chain.ClientInterface, logger *zap.Logger) *Service {
	service := &Service{
		config:            cfg,
		logger:            logger,
		client:            client,
		activeSecretCache: NewActiveSecretCache(logger, cfg.CacheMaxAge, cfg.CacheCleanupInterval),
		// A local health tracker by default; SetObservability swaps in the
		// monitoring service's shared one. Metrics stay nil-safe.
		health: monitoring.NewHealth(0),
	}
	service.startedAt = time.Now()
	service.observations = NewObservations(service.startedAt)

	service.registrationManager = NewRegistrationManager(cfg, client, logger)
	service.shareRevealService = NewShareRevealService(cfg, client, logger)
	// The reveal service is where confirmations and reveals actually happen, so
	// it holds the buffers the dashboard reads.
	service.shareRevealService.SetObservations(service.observations)
	service.eventMonitor = NewEventMonitor(cfg, logger, service.onChainEvent, service.onNewHeight)

	return service
}

// SetObservability wires the metrics and health sinks (nil-safe when absent).
// SetBuildInfo records facts only the start command knows: the resolved config
// path and the binary's version. Dashboard vitals show both, and neither is
// derivable from the config itself.
func (s *Service) SetBuildInfo(version, configPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = version
	s.configPath = configPath
}

func (s *Service) SetObservability(metrics *monitoring.Metrics, health *monitoring.Health) {
	s.metrics = metrics
	s.health = health
	s.shareRevealService.metrics = metrics
}

// VerifyRegistration checks chain connectivity, that the guardian is
// registered, and that the local share key matches the registered record
// (the same self-check Start enforces, surfaced as a pre-flight gate).
func (s *Service) VerifyRegistration(ctx context.Context) error {
	if err := s.client.Ping(ctx); err != nil {
		return fmt.Errorf("chain not reachable: %w", err)
	}

	guardian, err := s.client.GetGuardian(ctx, s.config.GuardianAddress)
	if err != nil {
		return fmt.Errorf("guardian not registered: %w", err)
	}

	return s.verifyShareKeyBinding(guardian)
}

// verifyShareKeyBinding is the startup self-check (key custody plan, Phase
// 2): the local share key must derive the on-chain registered public key, and
// the daemon refuses to run against one that does not — a wrong key otherwise
// surfaces only as decryption failures at confirmation time. A key that fails
// to LOAD is not a mismatch — that stays a health signal (SetKeyLoadable) so an
// operator can attach the passphrase file without a crash loop.
func (s *Service) verifyShareKeyBinding(guardian *chain.Guardian) error {
	privateKey, err := s.config.GetEncryptionPrivateKey()
	if err != nil {
		return nil // not loadable — handled by the health path, not here
	}
	derived, err := crypto.DerivePublicKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to derive public key from local share key: %w", err)
	}
	if !bytes.Equal(derived[:], guardian.EncryptionPublicKey) {
		return fmt.Errorf(
			"local share key at %s derives public key %x, but the registered guardian record holds %x — "+
				"running with the wrong key means missing every reveal (no-reveal slash on every assigned secret); "+
				"restore the correct key with 'guardiand key restore' before starting",
			s.config.EncryptionPrivateKeyPath, derived[:], guardian.EncryptionPublicKey)
	}
	return nil
}

// verifyEpochKeyring extends the startup self-check to the whole epoch
// keyring: derive the key epoch of every cached in-flight assignment (from
// its secret's creation height) and require each epoch's key to be locally
// loadable — the current key at the configured path, retired epochs beside
// it as <path>.epoch<N>. Restore missing epochs with 'guardiand key restore'
// (the backup bundle carries the whole keyring).
func (s *Service) verifyEpochKeyring(ctx context.Context) error {
	// An unloadable CURRENT key is a health signal, not a startup failure
	// (custody plan ruling: an operator attaching the passphrase file must
	// not fight a crash loop) — the keyring check only hard-fails when the
	// current key loads fine but a retired epoch's key is missing, which no
	// waiting-for-passphrase flow can fix.
	if _, err := s.config.GetEncryptionPrivateKey(); err != nil {
		return nil
	}

	resolver := s.shareRevealService.keys
	needed := map[uint64]bool{}
	for secretID, cached := range s.activeSecretCache.GetAll() {
		epoch, err := resolver.EpochAt(ctx, cached.Secret.CreatedAt)
		if err != nil {
			s.logger.Warn("Could not derive the key epoch for an in-flight assignment — the trial-decrypt fallback will cover it",
				zap.String("secret_id", secretID),
				zap.Int64("creation_height", cached.Secret.CreatedAt),
				zap.Error(err))
			continue
		}
		needed[epoch] = true
	}
	if len(needed) == 0 {
		return nil
	}

	if missing := resolver.MissingEpochKeys(ctx, needed); len(missing) > 0 {
		return fmt.Errorf(
			"in-flight assignments were encrypted to key epoch(s) %v but those keys are not locally available "+
				"(current key at %s, retired epochs beside it as .epoch<N>) — every such assignment will be "+
				"no-reveal slashed at its window; restore them with 'guardiand key restore' before starting",
			missing, s.config.EncryptionPrivateKeyPath)
	}
	return nil
}

// Start starts the guardian service and blocks until ctx is cancelled.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.isRunning {
		s.mu.Unlock()
		return fmt.Errorf("guardian service is already running")
	}
	s.isRunning = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}()

	s.logger.Info("Starting guardian service",
		zap.String("guardian_id", s.config.EffectiveGuardianID()))

	// Verify chain connectivity
	if err := s.client.Ping(ctx); err != nil {
		s.health.SetChainReachable(false)
		return fmt.Errorf("chain not reachable: %w", err)
	}

	s.logger.Info("Guardian address from config", zap.String("address", s.config.GuardianAddress))

	// Check registration status
	guardian, err := s.client.GetGuardian(ctx, s.config.GuardianAddress)
	if err != nil {
		return fmt.Errorf("guardian is not registered: %w", err)
	}

	// Refuse to run against the wrong share key — every reveal would be
	// missed and no-reveal slashed (startup self-check, key custody plan).
	if err := s.verifyShareKeyBinding(guardian); err != nil {
		s.logger.Error("Share key does not match the registered guardian record", zap.Error(err))
		return err
	}

	s.mu.Lock()
	s.isRegistered = true
	s.mu.Unlock()
	s.health.SetRegistered(true)

	s.logger.Info("Guardian found in registry",
		zap.String("stake", guardian.Stake.Amount+" "+guardian.Stake.Denom),
		zap.Bool("accepting_secrets", guardian.AcceptingSecrets))

	// Verify the share-decryption key loads — a guardian that cannot decrypt
	// shares must report unhealthy, not fail at first assignment.
	if _, err := s.config.GetEncryptionPrivateKey(); err != nil {
		s.health.SetKeyLoadable(false)
		s.logger.Error("Share-decryption key is not loadable", zap.Error(err))
	} else {
		s.health.SetKeyLoadable(true)
	}

	// Initialize active secret cache at startup
	currentHeight, err := s.client.GetCurrentBlockHeight(ctx)
	if err != nil {
		s.logger.Warn("Failed to get current height for cache initialization", zap.Error(err))
		currentHeight = 0 // Use 0 as fallback
	} else {
		s.lastKnownHeight.Store(currentHeight)
		s.health.RecordHeight(currentHeight)
	}

	if err := s.activeSecretCache.Initialize(ctx, s.client, s.config.GuardianAddress, currentHeight); err != nil {
		s.logger.Warn("Failed to initialize active secret cache", zap.Error(err))
		// Continue startup - cache will be populated during polling
	}

	// Epoch-keyring self-check (key rotation): every epoch with an in-flight
	// assignment must have its key locally available — refuse to run loudly
	// otherwise, since each such assignment would otherwise become a missed
	// reveal (no-reveal slash) at its window.
	if err := s.verifyEpochKeyring(ctx); err != nil {
		s.logger.Error("Epoch keyring is incomplete for in-flight assignments", zap.Error(err))
		return err
	}

	s.health.SetChainReachable(true)
	s.logger.Info("Guardian service started successfully")

	// Event-driven operation: react to chain events and block headers as the
	// primary signal; the polling loop below stays as the fallback/safety
	// net (and the only path when event monitoring is disabled).
	if s.config.EnableEventMonitoring {
		go s.eventMonitor.Run(ctx)
	}

	// Run monitoring loop (blocking)
	s.runSecretMonitoring(ctx)

	// Return context error when stopped
	return ctx.Err()
}

// Stop stops the guardian service
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isRunning {
		return nil
	}

	s.logger.Info("Stopping guardian service")
	s.isRunning = false

	// Loops stop when the run context is cancelled
	s.logger.Info("Guardian service stopped")
	return nil
}

// RegistrationOptions contains advanced registration parameters
type RegistrationOptions struct {
	StakeAmount            string
	AvailableFrom          int64
	AvailableUntil         int64
	IsUpdate               bool
	EncryptionPublicKeyHex string
	Force                  bool
}

// RegisterWithOptions registers the guardian with advanced options
func (s *Service) RegisterWithOptions(ctx context.Context, opts *RegistrationOptions) error {
	return s.registrationManager.RegisterWithOptions(ctx, opts)
}

// GetStatus returns the current guardian status
func (s *Service) GetStatus(ctx context.Context, detailed bool) (*Status, error) {
	s.mu.RLock()
	registered := s.isRegistered
	s.mu.RUnlock()

	status := &Status{
		GuardianID: s.config.EffectiveGuardianID(),
		Address:    s.config.GuardianAddress, // Always available from config
		ChainID:    s.config.ChainID,
		Registered: registered,
		LastUpdate: time.Now(),
	}

	// Get current block height
	if height, err := s.client.GetCurrentBlockHeight(ctx); err != nil {
		s.logger.Debug("Failed to get current block height", zap.Error(err))
		status.BlockHeight = -1 // Indicate unavailable
	} else {
		status.BlockHeight = height
		status.Healthy = true
	}

	// Get balance
	if balance, err := s.client.GetBalance(ctx, s.config.GuardianAddress, s.config.Denom); err != nil {
		s.logger.Debug("Failed to get balance", zap.Error(err))
		status.Balance = "unavailable"
	} else {
		status.Balance = balance.Amount + balance.Denom
	}

	// Check registration status and get guardian information
	if guardian, err := s.client.GetGuardian(ctx, s.config.GuardianAddress); err != nil {
		s.logger.Debug("Failed to get guardian info, likely not registered", zap.Error(err))
		status.Registered = false
	} else {
		status.Registered = true
		status.AvailableFrom = guardian.AvailableFrom
		status.AvailableUntil = guardian.AvailableUntil
		status.StakeAmount = guardian.Stake.Amount
		status.StakeDenom = guardian.Stake.Denom
		status.LockedStake = guardian.LockedStake.Amount
		status.AcceptingSecrets = guardian.AcceptingSecrets
		status.EncryptionPublicKey = fmt.Sprintf("%x", guardian.EncryptionPublicKey)

		if status.BlockHeight > 0 {
			status.Available = guardian.AvailableAt(status.BlockHeight)
		}
	}

	// Get active secrets from cache if registered
	if status.Registered {
		status.ActiveSecrets = s.activeSecretCache.Size()
	}

	return status, nil
}

// runSecretMonitoring runs the fallback polling loop. With event monitoring
// enabled this is the safety net that catches anything the subscriptions
// missed; without it, it is the primary discovery path.
func (s *Service) runSecretMonitoring(ctx context.Context) {
	ticker := time.NewTicker(s.config.PollingInterval)
	defer ticker.Stop()

	s.logger.Info("Starting secret monitoring",
		zap.Duration("interval", s.config.PollingInterval),
		zap.Bool("event_driven", s.config.EnableEventMonitoring))

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Secret monitoring stopped")
			return
		case <-ticker.C:
			if err := s.processSecrets(ctx); err != nil {
				s.metrics.RecordError()
				s.logger.Error("Error processing secrets", zap.Error(err))
			}
		}
	}
}

// processSecrets runs one full cycle: refresh height, resync the cache, act
// on confirmations and reveals. A failed height query does not skip the whole
// tick: the last known height keeps confirmations and reveals moving.
func (s *Service) processSecrets(ctx context.Context) error {
	started := time.Now()

	currentHeight, err := s.client.GetCurrentBlockHeight(ctx)
	if err != nil {
		currentHeight = s.lastKnownHeight.Load()
		if currentHeight == 0 {
			s.health.SetChainReachable(false)
			return fmt.Errorf("failed to get current height and no known height yet: %w", err)
		}
		s.logger.Warn("Height query failed — degrading to last known height",
			zap.Int64("last_known_height", currentHeight),
			zap.Error(err))
	} else {
		s.lastKnownHeight.Store(currentHeight)
		s.health.RecordHeight(currentHeight)
	}

	// Update cache from blockchain (idempotent)
	if err := s.activeSecretCache.UpdateFromBlockchain(ctx, s.client, s.config.GuardianAddress, currentHeight); err != nil {
		s.health.SetChainReachable(false)
		return fmt.Errorf("failed to update active secret cache: %w", err)
	}

	s.health.RecordPoll()

	// Drop in-flight records the chain has now caught up with, before acting on
	// the refreshed cache.
	s.reconcileInFlight()

	// Process secrets needing confirmation
	s.processConfirmations(ctx, currentHeight)

	// Process secrets needing reveal
	s.processReveals(ctx, currentHeight)

	s.metrics.RecordProcessingCycle(time.Since(started), s.activeSecretCache.Size(), currentHeight)
	return nil
}

// reconcileInFlight clears submissions the chain has now recorded, so a
// reservation does not hold its slot for the full expiry once the work it
// describes is visibly done.
//
// A secret absent from the refreshed cache has gone terminal or is no longer
// ours; either way nothing is outstanding for it. A secret still present is
// judged on what the chain says about our own assignment, which is the same
// evidence the cache uses to decide there is work to do.
func (s *Service) reconcileInFlight() {
	cached := s.activeSecretCache.GetAll()
	s.shareRevealService.InFlight().Retain(func(secretID string, kind SubmissionKind) bool {
		c, ok := cached[secretID]
		if !ok {
			return false
		}
		switch kind {
		case SubmissionConfirm:
			// Any status other than PROPOSED means our accept or reject landed.
			return c.Assignment.Status == "ASSIGNMENT_STATUS_PROPOSED"
		case SubmissionReveal:
			return !c.Secret.HasRevealed(c.Assignment.GuardianAddress)
		}
		return true
	})
}

// processConfirmations acts on every cached secret awaiting confirmation.
func (s *Service) processConfirmations(ctx context.Context, currentHeight int64) {
	confirmationSecrets := s.activeSecretCache.GetSecretsNeedingConfirmation()
	if len(confirmationSecrets) == 0 {
		return
	}
	s.logger.Debug("Processing confirmation secrets",
		zap.Int("confirmation_count", len(confirmationSecrets)),
		zap.Int64("current_height", currentHeight))

	for _, cached := range confirmationSecrets {
		if err := s.shareRevealService.ProcessConfirmation(ctx, cached.Secret, cached.Assignment, currentHeight); err != nil {
			s.metrics.RecordError()
			s.logger.Error("Failed to process secret confirmation",
				zap.String("secret_id", cached.Secret.ID),
				zap.Int64("height", currentHeight),
				zap.Error(err))
		}
	}
}

// processReveals submits due reveals with bounded parallelism — many secrets
// sharing one window must not overrun it by revealing serially. The client
// serialises signing per account, so parallel workers pipeline the
// decrypt/validate work while submissions stay ordered.
func (s *Service) processReveals(ctx context.Context, currentHeight int64) {
	revealSecrets := s.activeSecretCache.GetSecretsNeedingReveal(currentHeight)
	if len(revealSecrets) == 0 {
		return
	}
	s.logger.Debug("Processing reveal secrets",
		zap.Int("reveal_count", len(revealSecrets)),
		zap.Int64("current_height", currentHeight))

	sem := make(chan struct{}, s.config.MaxParallelReveals)
	var wg sync.WaitGroup
	for _, cached := range revealSecrets {
		wg.Add(1)
		sem <- struct{}{}
		go func(cached *CachedSecret) {
			defer wg.Done()
			defer func() { <-sem }()

			err := s.shareRevealService.ProcessReveal(ctx, cached.Secret, cached.Assignment, currentHeight)
			if err != nil {
				s.metrics.RecordError()
				s.logger.Error("Failed to process secret reveal",
					zap.String("secret_id", cached.Secret.ID),
					zap.Int64("height", currentHeight),
					zap.Int64("reveal_start", cached.Secret.RevealStartBlock),
					zap.Int64("reveal_end", cached.Secret.RevealEndBlock),
					zap.Error(err))
				return
			}
			// Mark secret as revealed in cache on successful reveal
			s.activeSecretCache.MarkRevealed(cached.Secret.ID)
		}(cached)
	}
	wg.Wait()
}

// onNewHeight reacts to a block-header event: reveals due at this exact
// height fire immediately instead of waiting for the next poll tick.
func (s *Service) onNewHeight(ctx context.Context, height int64) {
	s.lastKnownHeight.Store(height)
	s.health.RecordEvent()
	s.health.RecordHeight(height)
	s.processReveals(ctx, height)
	s.checkMissedWindows(height)
}

// onChainEvent reacts to a secrets-module transaction event: resync the
// cache and act on anything newly assigned to us.
func (s *Service) onChainEvent(ctx context.Context) {
	s.health.RecordEvent()
	if err := s.processSecrets(ctx); err != nil {
		s.metrics.RecordError()
		s.logger.Error("Error processing secrets after chain event", zap.Error(err))
	}
}

// checkMissedWindows emits the leading-indicator signal for reveal windows
// that closed without our reveal (including windows missed while down).
func (s *Service) checkMissedWindows(currentHeight int64) {
	for secretID, cached := range s.activeSecretCache.GetAll() {
		if cached.LocalState == StateNeedsReveal && currentHeight > cached.Secret.RevealEndBlock {
			s.metrics.RecordWindowMissed()
			s.logger.Error("Reveal window closed without our reveal — expect a no-reveal slash at settlement",
				zap.String("secret_id", secretID),
				zap.Int64("reveal_end", cached.Secret.RevealEndBlock),
				zap.Int64("current_height", currentHeight))
			s.activeSecretCache.EvictSecret(secretID)
		}
	}
}
