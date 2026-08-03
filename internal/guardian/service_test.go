package guardian

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/guardian/mocks"
)

func TestServiceCreation(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)
	assert.NotNil(t, service)

	// Verify basic initialization
	assert.Equal(t, cfg, service.config)
	assert.Equal(t, logger, service.logger)
	assert.NotNil(t, service.client)
	assert.NotNil(t, service.activeSecretCache)
	assert.NotNil(t, service.registrationManager)
	assert.NotNil(t, service.shareRevealService)
	assert.NotNil(t, service.eventMonitor)

	// Verify initial state
	assert.False(t, service.isRunning)
	assert.False(t, service.isRegistered)
}

func TestServiceVerifyRegistration_Success(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Add test guardian to chain (address must match config.GuardianAddress)
	guardian := mocks.CreateTestGuardian(cfg.GuardianAddress, true)
	mockChain.AddGuardian(guardian)

	service := NewServiceWithClient(cfg, mockClient, logger)

	require.NoError(t, service.VerifyRegistration(context.Background()))
}

func TestServiceVerifyRegistration_GuardianNotFound(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	// Don't add guardian to chain

	service := NewServiceWithClient(cfg, mockClient, logger)

	err := service.VerifyRegistration(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guardian not registered")
}

func TestServiceVerifyRegistration_ShareKeyMismatch(t *testing.T) {
	// Startup self-check (key custody plan, Phase 2): a loadable local key
	// that does NOT derive the registered public key must refuse to run.
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// On-chain record with a real public key…
	guardian, _, err := mocks.CreateTestGuardianWithRealKeypair(cfg.GuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)

	// …but a locally cached key from a DIFFERENT keypair.
	wrongKeypair, err := crypto.GenerateKeypair()
	require.NoError(t, err)
	cfg.SetEncryptionPrivateKey(wrongKeypair.PrivateKey)

	service := NewServiceWithClient(cfg, mockClient, logger)

	err = service.VerifyRegistration(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registered guardian record")

	// Start must refuse too, and never mark the service running.
	err = service.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registered guardian record")
	assert.False(t, service.isRunning)
}

func TestServiceVerifyRegistration_ShareKeyMatch(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	guardian, privateKey, err := mocks.CreateTestGuardianWithRealKeypair(cfg.GuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)
	cfg.SetEncryptionPrivateKey(privateKey)

	service := NewServiceWithClient(cfg, mockClient, logger)
	require.NoError(t, service.VerifyRegistration(context.Background()))
}

func TestServiceVerifyRegistration_ChainUnreachable(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Simulate chain connectivity failure
	mockClient.SetPingError(fmt.Errorf("connection refused"))

	service := NewServiceWithClient(cfg, mockClient, logger)

	err := service.VerifyRegistration(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chain not reachable")
}

func TestServiceStart_Success(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	cfg.EnableEventMonitoring = false // no websocket in unit tests
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	mocks.SetupTestScenario(mockChain)
	guardian := mocks.CreateTestGuardian(cfg.GuardianAddress, true)
	mockChain.AddGuardian(guardian)

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- service.Start(ctx)
	}()

	// Wait a bit for service to start
	time.Sleep(500 * time.Millisecond)

	service.mu.RLock()
	running := service.isRunning
	registered := service.isRegistered
	service.mu.RUnlock()

	assert.True(t, running)
	assert.True(t, registered)
	assert.Equal(t, cfg.GuardianAddress, service.config.GuardianAddress)

	// The write lock must NOT be held while the service runs — GetStatus on a
	// running instance used to deadlock (§6.6 of the improvements plan).
	statusCtx, statusCancel := context.WithTimeout(context.Background(), time.Second)
	defer statusCancel()
	_, err := service.GetStatus(statusCtx, false)
	assert.NoError(t, err, "GetStatus must not block while the service runs")

	cancel()

	select {
	case err := <-errCh:
		assert.True(t, err == context.Canceled || err == context.DeadlineExceeded, "Expected context cancellation or timeout, got: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Service did not stop within timeout")
	}
}

func TestServiceStart_RegistrationCheckFails(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	// Don't add guardian to chain

	service := NewServiceWithClient(cfg, mockClient, logger)

	err := service.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guardian is not registered")
	assert.False(t, service.isRunning)
}

func TestServiceStart_AlreadyRunning(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)
	service.isRunning = true // Already running

	err := service.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestServiceStop(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)
	service.isRunning = true

	require.NoError(t, service.Stop(context.Background()))
	assert.False(t, service.isRunning)
}

func TestServiceStop_NotRunning(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)
	service.isRunning = false

	// Should not error when stopping a non-running service
	require.NoError(t, service.Stop(context.Background()))
}

func TestServiceGetStatus_NotRegistered(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)

	status, err := service.GetStatus(context.Background(), false)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, cfg.EffectiveGuardianID(), status.GuardianID)
	assert.Equal(t, cfg.ChainID, status.ChainID)
	assert.False(t, status.Registered)
	assert.False(t, status.Available)
	assert.Equal(t, 0, status.ActiveSecrets)
}

func TestServiceGetStatus_Registered(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	mocks.SetupTestScenario(mockChain)

	service := NewServiceWithClient(cfg, mockClient, logger)
	service.isRegistered = true

	ctx := context.Background()
	require.NoError(t, service.activeSecretCache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight()))

	status, err := service.GetStatus(ctx, false)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.True(t, status.Registered)
	assert.Greater(t, status.ActiveSecrets, 0) // Should have active secrets from test scenario
	assert.Equal(t, "10000000000", status.StakeAmount)
	assert.Equal(t, "uveil", status.StakeDenom)
	assert.True(t, status.Available, "guardian availability window covers the mock height")
}

// TestServiceProcessSecrets_HeightFallback pins the §4.3 fix: one failed
// height query must not skip the whole tick — the last known height keeps
// confirmations and reveals moving.
func TestServiceProcessSecrets_HeightFallback(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)
	service.lastKnownHeight.Store(42)
	mockClient.SetGetHeightError(fmt.Errorf("rpc blip"))

	// With a last-known height the tick proceeds (cache resync runs, no error)
	require.NoError(t, service.processSecrets(context.Background()))
}

func TestServiceProcessSecrets_NoHeightAtAll(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewServiceWithClient(cfg, mockClient, logger)
	mockClient.SetGetHeightError(fmt.Errorf("rpc down"))

	// Never observed a height: nothing sane to do — surface the error
	err := service.processSecrets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no known height")
}

func TestServiceProcessSecrets_BlockchainError(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	mockClient.SetListSecretsError(fmt.Errorf("blockchain unavailable"))

	service := NewServiceWithClient(cfg, mockClient, logger)

	err := service.processSecrets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update active secret cache")
}
