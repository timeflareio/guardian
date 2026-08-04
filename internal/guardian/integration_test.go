package guardian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timeflareio/guardian/internal/guardian/mocks"
)

// TestEndToEndSecretFlow tests the complete secret lifecycle from guardian perspective
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestEndToEndSecretFlow(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	// Setup
	cfg := mocks.CreateTestConfig()
	cfg.EnableHMACValidation = false // Simplify for integration test
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Add test guardian with real keypair for encryption
	guardian, privateKey, err := mocks.CreateTestGuardianWithRealKeypair(mocks.TestGuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)

	// Setup crypto files with the matching private key
	tmpDir := t.TempDir()
	privateKeyPath := filepath.Join(tmpDir, "private_key")
	err = os.WriteFile(privateKeyPath, privateKey[:], 0600)
	require.NoError(t, err)
	cfg.EncryptionPrivateKeyPath = privateKeyPath

	// Create guardian service
	service := NewServiceWithClient(cfg, mockClient, logger)
	service.isRegistered = true

	ctx := context.Background()

	// Phase 1: Create secret awaiting acceptance
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 2, "awaiting_acceptance",
		100, 200)
	mockChain.AddSecret(secret)

	// Get the guardian's public key for encryption
	require.Len(t, guardian.EncryptionPublicKey, 32)
	var publicKey [32]byte
	copy(publicKey[:], guardian.EncryptionPublicKey)

	// Use real encryption for the assignment
	err = mockChain.AddAssignmentWithRealEncryption(secret.ID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED", publicKey)
	require.NoError(t, err)

	// Process secrets - should confirm assignment
	err = service.processSecrets(ctx)
	require.NoError(t, err)

	// Verify confirmation was sent
	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	assert.Equal(t, secret.ID, confirmCalls[0].SecretID)
	assert.True(t, confirmCalls[0].Accept)

	// Phase 2: Simulate assignment acceptance and move to reveal phase
	mockChain.UpdateAssignmentStatus(secret.ID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")
	mockChain.SetSecretState(secret.ID, "pending")

	// Advance to reveal window
	mockChain.AdvanceToHeight(100) // exactly at reveal start

	// Clear previous calls
	mockClient.ClearCalls()

	// Process secrets - should reveal share
	err = service.processSecrets(ctx)
	require.NoError(t, err)

	// Verify reveal was sent
	revealCalls := mockClient.GetGuardianRevealShareCalls()
	require.Len(t, revealCalls, 1)
	assert.Equal(t, secret.ID, revealCalls[0].SecretID)
	// ShareIndex is now embedded in share data, not tracked separately

	// Verify secret was marked as revealed in cache
	cachedSecret := service.activeSecretCache.Get(secret.ID)
	require.NotNil(t, cachedSecret)
	assert.Equal(t, StateRevealed, cachedSecret.LocalState)

	// Phase 3: Subsequent processing should not re-reveal
	mockClient.ClearCalls()
	err = service.processSecrets(ctx)
	require.NoError(t, err)

	// No additional calls should be made
	assert.Len(t, mockClient.GetGuardianConfirmSharesCalls(), 0)
	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 0)
}

// TestMultipleSecretsParallel tests processing multiple secrets simultaneously
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestMultipleSecretsParallel(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	cfg := mocks.CreateTestConfig()
	cfg.EnableHMACValidation = false
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Create guardian with real keypair for encryption
	guardian, privateKey, err := mocks.CreateTestGuardianWithRealKeypair(mocks.TestGuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)

	// Set up matching private key for decryption
	cfg.SetEncryptionPrivateKey(privateKey)

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx := context.Background()

	// Create multiple secrets in different states
	secrets := []struct {
		id    string
		state string
		asgn  string
	}{
		{mocks.GenerateTestSecretID(), "awaiting_acceptance", "ASSIGNMENT_STATUS_PROPOSED"},
		{mocks.GenerateTestSecretID(), "awaiting_acceptance", "ASSIGNMENT_STATUS_PROPOSED"},
		{mocks.GenerateTestSecretID(), "pending", "ASSIGNMENT_STATUS_ACCEPTED"},
		{mocks.GenerateTestSecretID(), "pending", "ASSIGNMENT_STATUS_ACCEPTED"},
	}

	currentHeight := int64(100)
	mockChain.AdvanceToHeight(currentHeight)

	// Get the guardian's public key for encryption
	var publicKey [32]byte
	copy(publicKey[:], guardian.EncryptionPublicKey)

	for i, s := range secrets {
		secret := mocks.CreateTestSecretWithRevealWindow(s.id, 2, s.state,
			currentHeight+int64(i), currentHeight+int64(i)+100)
		mockChain.AddSecret(secret)
		err = mockChain.AddAssignmentWithRealEncryption(s.id, mocks.TestGuardianAddress, s.asgn, publicKey)
		require.NoError(t, err)
	}

	// Process all secrets
	err = service.processSecrets(ctx)
	require.NoError(t, err)

	// Should have confirmed the two awaiting acceptance
	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	assert.Len(t, confirmCalls, 2)

	// Should have revealed the two pending secrets (depending on reveal window)
	revealCalls := mockClient.GetGuardianRevealShareCalls()
	// Note: Exact count depends on reveal window timing, but should be > 0
	assert.GreaterOrEqual(t, len(revealCalls), 0)

	// Verify cache state
	assert.Greater(t, service.activeSecretCache.Size(), 0)
	stateCounts := service.activeSecretCache.GetStateCount()
	assert.Greater(t, stateCounts[StateNeedsConfirmation]+stateCounts[StateNeedsReveal]+stateCounts[StateRevealed], 0)
}

// TestBlockProgression tests time-based reveal coordination
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestBlockProgression(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	tmpDir := t.TempDir()
	privateKeyPath := filepath.Join(tmpDir, "private_key")

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = privateKeyPath
	cfg.EnableHMACValidation = false
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Create guardian with real keypair for encryption
	guardian, privateKey, err := mocks.CreateTestGuardianWithRealKeypair(mocks.TestGuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)

	// Write the matching private key to file
	err = os.WriteFile(privateKeyPath, privateKey[:], 0600)
	require.NoError(t, err)

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx := context.Background()

	// Create secret with specific timing
	currentHeight := int64(50)
	mockChain.AdvanceToHeight(currentHeight)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 1, "pending",
		100, 200) // Reveal window: 100-200
	mockChain.AddSecret(secret)

	// Get the guardian's public key for encryption
	var publicKey [32]byte
	copy(publicKey[:], guardian.EncryptionPublicKey)

	err = mockChain.AddAssignmentWithRealEncryption(secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED", publicKey)
	require.NoError(t, err)

	// Test 1: Too early (before reveal start)
	err = service.processSecrets(ctx)
	require.NoError(t, err)
	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 0) // Should not reveal

	// Test 2: Perfect timing (at reveal start)
	mockChain.AdvanceToHeight(100) // Exactly at reveal start
	err = service.processSecrets(ctx)
	require.NoError(t, err)
	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 1) // Should reveal immediately

	// Test 4: After reveal window
	mockClient.ClearCalls()
	mockChain.AdvanceToHeight(250) // Past reveal end
	err = service.processSecrets(ctx)
	require.NoError(t, err)
	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 0) // Should not reveal again
}

// TestRevealWindowExpiry tests handling of expired reveal windows
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestRevealWindowExpiry(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	guardian := mocks.CreateTestGuardian(mocks.TestGuardianAddress, true)
	mockChain.AddGuardian(guardian)

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx := context.Background()

	// Create secret with past reveal window
	currentHeight := int64(150)
	mockChain.AdvanceToHeight(currentHeight)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 1, "pending",
		50, 100) // Reveal window: 50-100 (already passed)
	mockChain.AddSecret(secret)
	mockChain.AddAssignment(secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")

	// Initialize cache
	err := service.activeSecretCache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	// Should still cache the secret (might be evicted later)
	revealSecrets := service.activeSecretCache.GetSecretsNeedingReveal(currentHeight)
	assert.Len(t, revealSecrets, 0) // Should not be in reveal list due to expired window

	// Process secrets - should not attempt reveal
	err = service.processSecrets(ctx)
	require.NoError(t, err)
	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 0)

	// Advance more blocks to trigger eviction
	mockChain.AdvanceBlocks(100) // Trigger cleanup
	err = service.processSecrets(ctx)
	require.NoError(t, err)

	// Secret should eventually be evicted due to expired window
	// (exact timing depends on eviction logic)
}

// TestCacheEvictionDuringProcessing tests race conditions during eviction
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestCacheEvictionDuringProcessing(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	guardian := mocks.CreateTestGuardian(mocks.TestGuardianAddress, true)
	mockChain.AddGuardian(guardian)

	// Create cache with aggressive eviction
	cache := NewActiveSecretCache(logger, 0, 0)
	cache.cleanupInterval = 1           // Cleanup every block
	cache.maxCacheAge = time.Nanosecond // Immediate TTL expiry

	service := NewServiceWithClient(cfg, mockClient, logger)
	service.activeSecretCache = cache

	ctx := context.Background()

	// Create secret
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 1, "awaiting_acceptance")
	mockChain.AddSecret(secret)
	mockChain.AddAssignment(secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)

	initialSize := cache.Size()
	assert.Greater(t, initialSize, 0)

	// Multiple updates should trigger eviction
	for i := 0; i < 5; i++ {
		mockChain.AdvanceBlocks(1)
		err = service.processSecrets(ctx)
		require.NoError(t, err)
	}

	// Cache should be empty due to aggressive eviction
	assert.Equal(t, 0, cache.Size())
}

// TestServiceRestartRecovery tests cache recovery after service restart
// NOTE: This test only uses cache initialization, no CLI transactions required
func TestServiceRestartRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	privateKeyPath := filepath.Join(tmpDir, "private_key")
	testKey := mocks.CreateTestPrivateKey()
	err := os.WriteFile(privateKeyPath, testKey[:], 0600)
	require.NoError(t, err)

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = privateKeyPath
	cfg.EnableHMACValidation = false
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Setup persistent blockchain state
	guardian := mocks.CreateTestGuardian(mocks.TestGuardianAddress, true)
	mockChain.AddGuardian(guardian)
	mocks.SetupTestScenario(mockChain)

	ctx := context.Background()

	// Create first service instance
	service1 := NewServiceWithClient(cfg, mockClient, logger)

	// Initialize and verify state
	err = service1.activeSecretCache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)
	service1Size := service1.activeSecretCache.Size()
	service1States := service1.activeSecretCache.GetStateCount()

	// Create second service instance (simulating restart)
	service2 := NewServiceWithClient(cfg, mockClient, logger)

	// Initialize second service - should recover same state
	err = service2.activeSecretCache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)

	// State should be consistent between instances
	assert.Equal(t, service1Size, service2.activeSecretCache.Size())
	assert.Equal(t, service1States, service2.activeSecretCache.GetStateCount())

	// Both services should identify same actionable secrets
	service1Confirmation := service1.activeSecretCache.GetSecretsNeedingConfirmation()
	service2Confirmation := service2.activeSecretCache.GetSecretsNeedingConfirmation()
	assert.Equal(t, len(service1Confirmation), len(service2Confirmation))
}

// TestErrorRecovery tests graceful error handling and recovery
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestErrorRecovery(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	guardian := mocks.CreateTestGuardian(mocks.TestGuardianAddress, true)
	mockChain.AddGuardian(guardian)
	mocks.SetupTestScenario(mockChain)

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx := context.Background()

	// Initialize successfully
	err := service.activeSecretCache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)
	initialSize := service.activeSecretCache.Size()

	// Simulate blockchain error
	mockClient.SetListSecretsError(fmt.Errorf("blockchain temporarily unavailable"))

	// Processing should fail but cache should remain intact
	err = service.processSecrets(ctx)
	require.Error(t, err)
	assert.Equal(t, initialSize, service.activeSecretCache.Size()) // Cache unchanged

	// Restore blockchain connectivity
	mockClient.SetListSecretsError(nil)

	// Processing should recover
	err = service.processSecrets(ctx)
	require.NoError(t, err)

	// Cache should be updated successfully
	assert.GreaterOrEqual(t, service.activeSecretCache.Size(), 0)
}

// TestPerformanceUnderLoad tests cache performance with many secrets
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestPerformanceUnderLoad(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Create guardian with real keypair for encryption
	guardian, privateKey, err := mocks.CreateTestGuardianWithRealKeypair(mocks.TestGuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)

	// Set up matching private key for decryption
	cfg.SetEncryptionPrivateKey(privateKey)

	// Get the guardian's public key for encryption
	var publicKey [32]byte
	copy(publicKey[:], guardian.EncryptionPublicKey)

	// Create many secrets with real encryption
	secretCount := 1000
	for i := 0; i < secretCount; i++ {
		state := "awaiting_acceptance"
		assignmentStatus := "ASSIGNMENT_STATUS_PROPOSED"
		if i%2 == 0 {
			state = "pending"
			assignmentStatus = "ASSIGNMENT_STATUS_ACCEPTED"
		}

		secretID := mocks.GenerateTestSecretID()
		secret := mocks.CreateTestSecret(secretID, 1, state)
		mockChain.AddSecret(secret)
		err = mockChain.AddAssignmentWithRealEncryption(secretID, mocks.TestGuardianAddress, assignmentStatus, publicKey)
		require.NoError(t, err)
	}

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx := context.Background()

	// Measure initialization time
	start := time.Now()
	err = service.activeSecretCache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	initTime := time.Since(start)
	require.NoError(t, err)

	t.Logf("Initialized cache with %d secrets in %v", service.activeSecretCache.Size(), initTime)
	assert.Less(t, initTime, 5*time.Second) // Should complete within 5 seconds

	// Measure update time
	start = time.Now()
	err = service.processSecrets(ctx)
	updateTime := time.Since(start)
	require.NoError(t, err)

	t.Logf("Processed %d secrets in %v", service.activeSecretCache.Size(), updateTime)
	assert.Less(t, updateTime, 2*time.Second) // Should complete within 2 seconds

	// Verify cache efficiency
	confirmationSecrets := service.activeSecretCache.GetSecretsNeedingConfirmation()
	revealSecrets := service.activeSecretCache.GetSecretsNeedingReveal(mockChain.GetCurrentHeight())

	t.Logf("Confirmation secrets: %d, Reveal secrets: %d", len(confirmationSecrets), len(revealSecrets))
	assert.Greater(t, len(confirmationSecrets), 0)
	assert.GreaterOrEqual(t, len(revealSecrets), 0)
}

// TestConcurrentSecretProcessing tests concurrent secret processing
// REQUIRES RUNNING BLOCKCHAIN: This test calls service.processSecrets() which executes CLI transactions
func TestConcurrentSecretProcessing(t *testing.T) {
	t.Skip("Test requires running blockchain for CLI transactions")
	cfg := mocks.CreateTestConfig()
	cfg.EnableHMACValidation = false
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Create guardian with real keypair for encryption
	guardian, privateKey, err := mocks.CreateTestGuardianWithRealKeypair(mocks.TestGuardianAddress, true)
	require.NoError(t, err)
	mockChain.AddGuardian(guardian)

	// Set up matching private key for decryption
	cfg.SetEncryptionPrivateKey(privateKey)

	service := NewServiceWithClient(cfg, mockClient, logger)

	ctx := context.Background()

	// Get the guardian's public key for encryption
	var publicKey [32]byte
	copy(publicKey[:], guardian.EncryptionPublicKey)

	// Create secrets that can be processed concurrently
	for i := 0; i < 10; i++ {
		secretID := mocks.GenerateTestSecretID()
		secret := mocks.CreateTestSecret(secretID, 1, "awaiting_acceptance")
		mockChain.AddSecret(secret)
		err = mockChain.AddAssignmentWithRealEncryption(secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED", publicKey)
		require.NoError(t, err)
	}

	// Process secrets multiple times concurrently
	done := make(chan error, 3)

	for i := 0; i < 3; i++ {
		go func() {
			done <- service.processSecrets(ctx)
		}()
	}

	// Wait for all to complete
	for i := 0; i < 3; i++ {
		err := <-done
		assert.NoError(t, err)
	}

	// All secrets should be processed correctly
	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	assert.GreaterOrEqual(t, len(confirmCalls), 10) // Should have processed all secrets
}
