package guardian

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/guardian/mocks"
)

func TestNewActiveSecretCache(t *testing.T) {
	logger := mocks.CreateTestLogger()
	cache := NewActiveSecretCache(logger, 0, 0)

	assert.NotNil(t, cache)
	assert.Equal(t, 0, cache.Size())
	assert.Equal(t, int64(50), cache.cleanupInterval)
	assert.Equal(t, 7*24*time.Hour, cache.maxCacheAge)
}

func TestCacheStateTransitions(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Setup test scenario
	mocks.SetupTestScenario(mockChain)
	guardianAddr := mocks.TestGuardianAddress

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, guardianAddr, currentHeight)
	require.NoError(t, err)

	// Test 1: Secret needing confirmation
	confirmationSecrets := cache.GetSecretsNeedingConfirmation()
	assert.Greater(t, len(confirmationSecrets), 0)

	awaitingSecretID := mocks.GenerateDeterministicSecretID("test-awaiting-acceptance")
	foundAwaitingSecret := false
	for _, cached := range confirmationSecrets {
		if cached.Secret.ID == awaitingSecretID {
			assert.Equal(t, StateNeedsConfirmation, cached.LocalState)
			assert.Equal(t, "awaiting_acceptance", cached.Secret.State)
			assert.Equal(t, "ASSIGNMENT_STATUS_PROPOSED", cached.Assignment.Status)
			foundAwaitingSecret = true
			break
		}
	}
	assert.True(t, foundAwaitingSecret, "Should find awaiting-acceptance secret in confirmation state")

	// Test 2: Transition to reveal state
	// Simulate acceptance and move to reveal window
	mockChain.UpdateAssignmentStatus(awaitingSecretID, guardianAddr, "ASSIGNMENT_STATUS_ACCEPTED")
	mockChain.SetSecretState(awaitingSecretID, "pending")
	mocks.AdvanceToRevealWindow(mockChain, awaitingSecretID)

	// Update cache
	err = cache.UpdateFromBlockchain(ctx, mockClient, guardianAddr, mockChain.GetCurrentHeight())
	require.NoError(t, err)

	// Should now be in reveal state
	revealSecrets := cache.GetSecretsNeedingReveal(mockChain.GetCurrentHeight())
	foundRevealSecret := false
	for _, cached := range revealSecrets {
		if cached.Secret.ID == awaitingSecretID {
			assert.Equal(t, StateNeedsReveal, cached.LocalState)
			assert.Equal(t, "pending", cached.Secret.State)
			assert.Equal(t, "ASSIGNMENT_STATUS_ACCEPTED", cached.Assignment.Status)
			foundRevealSecret = true
			break
		}
	}
	assert.True(t, foundRevealSecret, "Should find awaiting-acceptance secret in reveal state")

	// Test 3: Mark as revealed
	cache.MarkRevealed(awaitingSecretID)

	// Should no longer be in reveal list
	revealSecrets = cache.GetSecretsNeedingReveal(mockChain.GetCurrentHeight())
	for _, cached := range revealSecrets {
		assert.NotEqual(t, awaitingSecretID, cached.Secret.ID)
	}

	// Should be marked as revealed in cache
	cachedSecret := cache.Get(awaitingSecretID)
	require.NotNil(t, cachedSecret)
	assert.Equal(t, StateRevealed, cachedSecret.LocalState)
}

func TestCacheIndexedAccess(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Create multiple secrets in different states
	guardianAddrs := []string{mocks.TestGuardianAddress}

	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "awaiting_acceptance")
	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "awaiting_acceptance")
	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "pending")

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	// Test indexed access
	confirmationSecrets := cache.GetSecretsNeedingConfirmation()
	_ = cache.GetSecretsNeedingReveal(currentHeight)

	// Should have 2 secrets needing confirmation
	assert.Equal(t, 2, len(confirmationSecrets))

	// Should have 1 secret needing reveal (if in reveal window)
	// Note: secret-3 might not be in reveal window yet depending on test timing

	// Verify state counts
	stateCounts := cache.GetStateCount()
	assert.Greater(t, stateCounts[StateNeedsConfirmation], 0)

	// Total size should match sum of all states
	totalSize := 0
	for _, count := range stateCounts {
		totalSize += count
	}
	assert.Equal(t, cache.Size(), totalSize)
}

func TestCacheConcurrentAccess(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Setup test scenario
	mocks.SetupTestScenario(mockChain)

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	// Test concurrent read/write operations
	done := make(chan bool, 4)

	// Concurrent readers
	go func() {
		for i := 0; i < 100; i++ {
			_ = cache.GetSecretsNeedingConfirmation()
			_ = cache.GetSecretsNeedingReveal(currentHeight)
			_ = cache.Size()
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			_ = cache.GetAll()
			_ = cache.GetStateCount()
		}
		done <- true
	}()

	// Concurrent writers
	go func() {
		for i := 0; i < 50; i++ {
			cache.MarkRevealed("secret-revealed")
			cache.EvictSecret("non-existent")
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 50; i++ {
			_ = cache.UpdateFromBlockchain(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}

	// Cache should still be functional after concurrent access
	assert.Greater(t, cache.Size(), 0)
}

func TestCacheInitialization(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Setup comprehensive test scenario
	guardianAddrs := []string{mocks.TestGuardianAddress}

	// Different secret states
	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "awaiting_acceptance")
	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "pending")
	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "revealed")
	mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "cancelled")

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	// Verify only actionable secrets are cached
	assert.Greater(t, cache.Size(), 0)

	// Should only cache actionable secrets (awaiting_acceptance and pending)
	// Revealed and cancelled secrets should not be in the cache
	stateCounts := cache.GetStateCount()
	assert.Greater(t, stateCounts[StateNeedsConfirmation], 0, "Should have secrets needing confirmation")

	// Total actionable secrets should be awaiting_acceptance + pending
	expectedActionable := stateCounts[StateNeedsConfirmation] + stateCounts[StateNeedsReveal]
	assert.Equal(t, expectedActionable, cache.Size(), "Cache should only contain actionable secrets")
}

func TestCacheIdempotentUpdates(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Setup test scenario
	mocks.SetupTestScenario(mockChain)

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	initialSize := cache.Size()
	initialState := cache.GetStateCount()

	// Multiple updates with same data should be idempotent
	for i := 0; i < 5; i++ {
		err = cache.UpdateFromBlockchain(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
		require.NoError(t, err)
	}

	// Size and state should remain the same
	assert.Equal(t, initialSize, cache.Size())
	assert.Equal(t, initialState, cache.GetStateCount())
}

func TestCacheAutomaticEviction(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)
	cache.cleanupInterval = 5 // Reduce for testing

	// Setup test scenario
	mocks.SetupTestScenario(mockChain)

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	initialSize := cache.Size()

	// Mark a secret as revealed (should become evictable)
	cache.MarkRevealed("secret-revealed")

	// Advance blocks to trigger cleanup
	mockChain.AdvanceBlocks(10)

	// Update cache to trigger cleanup
	err = cache.UpdateFromBlockchain(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)

	// Size should be smaller due to eviction
	assert.LessOrEqual(t, cache.Size(), initialSize)
}

func TestCacheTTLEviction(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)
	cache.maxCacheAge = 100 * time.Millisecond // Very short TTL for testing
	cache.cleanupInterval = 1                  // Trigger cleanup frequently

	// Setup test scenario
	mocks.SetupTestScenario(mockChain)

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	initialSize := cache.Size()
	assert.Greater(t, initialSize, 0)

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Advance blocks to trigger cleanup
	mockChain.AdvanceBlocks(2)

	// Update cache to trigger cleanup
	err = cache.UpdateFromBlockchain(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)

	// All items should be evicted due to TTL
	assert.Equal(t, 0, cache.Size())
}

func TestCacheRevealWindowValidation(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Create secret with specific reveal window
	currentHeight := mockChain.GetCurrentHeight()
	secret := mocks.CreateTestSecretWithRevealWindow("test-reveal", 1, "pending", currentHeight+10, currentHeight+20)
	mockChain.AddSecret(secret)
	mockChain.AddAssignment("test-reveal", mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")

	ctx := context.Background()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	// Before reveal window - should not be in reveal list
	revealSecrets := cache.GetSecretsNeedingReveal(currentHeight)
	assert.Equal(t, 0, len(revealSecrets))

	// At reveal start - should be in reveal list immediately
	targetHeight := currentHeight + 10 // reveal start
	revealSecrets = cache.GetSecretsNeedingReveal(targetHeight)
	foundSecret := false
	for _, cached := range revealSecrets {
		if cached.Secret.ID == "test-reveal" {
			foundSecret = true
			break
		}
	}
	assert.True(t, foundSecret, "Should be in reveal list during reveal window")

	// After reveal window - should not be in reveal list
	revealSecrets = cache.GetSecretsNeedingReveal(currentHeight + 25)
	assert.Equal(t, 0, len(revealSecrets))
}

func TestCacheMemoryUsage(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	// Create many secrets
	guardianAddrs := []string{mocks.TestGuardianAddress}
	for i := 0; i < 100; i++ {
		mocks.SetupMultiGuardianSecret(mockChain, 1, guardianAddrs, "awaiting_acceptance")
	}

	ctx := context.Background()
	currentHeight := mockChain.GetCurrentHeight()

	// Initialize cache
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, currentHeight)
	require.NoError(t, err)

	// Should cache all actionable secrets
	assert.Equal(t, 100, cache.Size())

	// Mark half of the secrets as revealed (evictable)
	// Get the actual secret IDs from the cache
	allSecrets := cache.GetAll()
	markedCount := 0
	for secretID := range allSecrets {
		if markedCount >= 50 {
			break
		}
		cache.MarkRevealed(secretID)
		markedCount++
	}

	// Force cleanup
	cache.cleanupInterval = 1
	mockChain.AdvanceBlocks(2)
	err = cache.UpdateFromBlockchain(ctx, mockClient, mocks.TestGuardianAddress, mockChain.GetCurrentHeight())
	require.NoError(t, err)

	// Should have evicted revealed secrets
	assert.Equal(t, 50, cache.Size())
}

func TestCacheErrorHandling(t *testing.T) {
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	cache := NewActiveSecretCache(logger, 0, 0)

	ctx := context.Background()

	// Test initialization with blockchain error
	mockClient.SetListSecretsError(fmt.Errorf("blockchain error"))
	err := cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch secrets for guardian")

	// Reset error
	mockClient.SetListSecretsError(nil)
	mocks.SetupTestScenario(mockChain)

	// Initialize successfully
	err = cache.Initialize(ctx, mockClient, mocks.TestGuardianAddress, 1)
	require.NoError(t, err)

	// Test update with blockchain error
	mockClient.SetListSecretsError(fmt.Errorf("update error"))
	err = cache.UpdateFromBlockchain(ctx, mockClient, mocks.TestGuardianAddress, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch secrets for guardian")

	// Cache should still be functional after error
	assert.Greater(t, cache.Size(), 0)
}

func TestCacheEdgeCases(t *testing.T) {
	logger := mocks.CreateTestLogger()
	cache := NewActiveSecretCache(logger, 0, 0)

	// Test operations on empty cache
	assert.Equal(t, 0, cache.Size())
	assert.Empty(t, cache.GetSecretsNeedingConfirmation())
	assert.Empty(t, cache.GetSecretsNeedingReveal(100))
	assert.Empty(t, cache.GetAll())

	// Test operations with non-existent secrets
	cache.MarkRevealed("non-existent")
	cache.EvictSecret("non-existent")
	assert.Nil(t, cache.Get("non-existent"))

	// Test state counts on empty cache
	stateCounts := cache.GetStateCount()
	for _, count := range stateCounts {
		assert.Equal(t, 0, count)
	}
}

// TestDetermineLocalState_ReconstructableStillNeedsReveal is the regression
// guard for the "slowest poller gets slashed" bug: once the reveal threshold
// is met mid-window the secret's state becomes "reconstructable", and the old
// cache logic evicted it — so any accepted guardian that had not yet revealed
// silently never did, and was slashed 50% of its bond as a no-show at
// settlement. The chain accepts reveals in both pending and reconstructable,
// and settlement pays every revealer, so the daemon must keep revealing.
func TestDetermineLocalState_ReconstructableStillNeedsReveal(t *testing.T) {
	logger := mocks.CreateTestLogger()
	cache := NewActiveSecretCache(logger, 0, 0)

	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")

	// Threshold already met (state reconstructable), we have NOT revealed:
	// must still be actionable
	secret := mocks.CreateTestSecretWithRevealWindow("reconstructable-unrevealed", 1, "reconstructable", 100, 200)
	state := cache.determineLocalState(secret, &assignment, 150)
	assert.Equal(t, StateNeedsReveal, state,
		"an accepted guardian that has not revealed must keep revealing after the threshold is met")

	// Same state but our share is already on-chain: nothing left to do
	revealed := mocks.CreateTestSecretWithRevealWindow("reconstructable-revealed", 1, "reconstructable", 100, 200)
	revealed.RevealedShares = []chain.RevealedShare{
		{GuardianAddress: mocks.TestGuardianAddress, RevealedAtBlock: 150},
	}
	state = cache.determineLocalState(revealed, &assignment, 155)
	assert.Equal(t, StateEvictable, state,
		"a guardian whose reveal is on-chain has nothing left to do")

	// Pending + already revealed must also evict (no re-reveal spam)
	pendingRevealed := mocks.CreateTestSecretWithRevealWindow("pending-revealed", 3, "pending", 100, 200)
	pendingRevealed.RevealedShares = []chain.RevealedShare{
		{GuardianAddress: mocks.TestGuardianAddress, RevealedAtBlock: 150},
	}
	state = cache.determineLocalState(pendingRevealed, &assignment, 155)
	assert.Equal(t, StateEvictable, state)
}

// TestIsInRevealWindow_EndInclusive pins the client to the chain's
// [start, end] window (both bounds inclusive): a reveal at height == end is
// valid on-chain, and settlement runs in the EndBlock of end + 1.
func TestIsInRevealWindow_EndInclusive(t *testing.T) {
	logger := mocks.CreateTestLogger()
	cache := NewActiveSecretCache(logger, 0, 0)

	secret := mocks.CreateTestSecretWithRevealWindow("window-bounds", 1, "pending", 100, 200)

	assert.False(t, cache.isInRevealWindow(secret, 99), "before start")
	assert.True(t, cache.isInRevealWindow(secret, 100), "start is inclusive")
	assert.True(t, cache.isInRevealWindow(secret, 200), "end is inclusive — last valid block")
	assert.False(t, cache.isInRevealWindow(secret, 201), "end + 1 is the settlement block")
}
