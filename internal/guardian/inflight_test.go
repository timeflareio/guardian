package guardian

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/guardian/mocks"
)

// confirmableAssignment builds an assignment whose share decrypts and whose
// HMAC verifies, so ProcessConfirmation reaches the accept broadcast.
func confirmableAssignment(t *testing.T, secretID string, pub [32]byte) chain.GuardianAssignment {
	t.Helper()

	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")

	shareData := []byte("test share data")
	encrypted, err := mocks.CreateProperlyEncryptedShareForTesting(shareData, pub)
	require.NoError(t, err)
	assignment.EncryptedShare = encrypted

	hmacBytes, err := crypto.GenerateHMAC(secretID, mocks.TestGuardianAddress, shareData)
	require.NoError(t, err)
	assignment.ShareHMAC = hmacBytes

	return assignment
}

func TestInFlight_SecondConfirmationInsideWindowIsSuppressed(t *testing.T) {
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, mockClient, logger)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := confirmableAssignment(t, secretID, testKeypair.PublicKey)

	ctx := context.Background()

	// The chain has not included the first accept, so the caller sees the same
	// assignment as unhandled on the very next cycle — the exact sequence that
	// cost a guardian a duplicate fee on every secret.
	require.NoError(t, service.ProcessConfirmation(ctx, secret, &assignment, 100))
	require.NoError(t, service.ProcessConfirmation(ctx, secret, &assignment, 101))
	require.NoError(t, service.ProcessConfirmation(ctx, secret, &assignment, 104))

	assert.Len(t, mockClient.GetGuardianConfirmSharesCalls(), 1,
		"only the first confirmation should reach the chain while one is in flight")
}

func TestInFlight_ConfirmationAfterExpiryIsAllowed(t *testing.T) {
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, mockClient, logger)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := confirmableAssignment(t, secretID, testKeypair.PublicKey)

	ctx := context.Background()

	require.NoError(t, service.ProcessConfirmation(ctx, secret, &assignment, 100))
	// A transaction dropped from the mempool never reconciles, so the guard has
	// to lapse or the guardian would wait out a window it could still have met.
	require.NoError(t, service.ProcessConfirmation(ctx, secret, &assignment, 100+inFlightExpiryBlocks))

	assert.Len(t, mockClient.GetGuardianConfirmSharesCalls(), 2,
		"the submission should be retried once the in-flight record has lapsed")
}

func TestInFlight_FailedBroadcastRetriesImmediately(t *testing.T) {
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, mockClient, logger)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := confirmableAssignment(t, secretID, testKeypair.PublicKey)

	ctx := context.Background()

	// Nothing reached the mempool, so nothing is outstanding: the reservation
	// must be released rather than blocking the retry for five blocks.
	mockClient.SetGuardianConfirmSharesError(errors.New("broadcast rejected at CheckTx"))
	require.Error(t, service.ProcessConfirmation(ctx, secret, &assignment, 100))
	require.Empty(t, mockClient.GetGuardianConfirmSharesCalls(),
		"the mock records only broadcasts that were accepted, so the failure leaves nothing behind")

	// Height 101 is well inside the five-block window the first attempt would
	// have claimed. That this reaches the chain at all is the proof the failed
	// reservation was released.
	mockClient.SetGuardianConfirmSharesError(nil)
	require.NoError(t, service.ProcessConfirmation(ctx, secret, &assignment, 101))

	calls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, calls, 1, "a failed broadcast must not suppress the retry")
	assert.True(t, calls[0].Accept)
}

func TestInFlight_ConfirmDoesNotBlockThatSecretsReveal(t *testing.T) {
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, mockClient, logger)

	secretID := mocks.GenerateTestSecretID()
	ctx := context.Background()

	confirmSecret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	confirmAssignment := confirmableAssignment(t, secretID, testKeypair.PublicKey)
	require.NoError(t, service.ProcessConfirmation(ctx, confirmSecret, &confirmAssignment, 100))

	// Same secret, different transaction at a different time: the accept being
	// outstanding says nothing about whether the reveal has been sent.
	currentHeight := mockChain.GetCurrentHeight()
	revealSecret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-10, currentHeight+20)
	revealAssignment, err := mocks.CreateTestAssignmentWithRealEncryption(
		secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED", testKeypair.PublicKey)
	require.NoError(t, err)

	require.NoError(t, service.ProcessReveal(ctx, revealSecret, &revealAssignment, currentHeight))

	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 1,
		"an accept in flight must not suppress that secret's reveal")
}

func TestInFlight_SecondRevealInsideWindowIsSuppressed(t *testing.T) {
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, mockClient, logger)

	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-10, currentHeight+20)
	assignment, err := mocks.CreateTestAssignmentWithRealEncryption(
		secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED", testKeypair.PublicKey)
	require.NoError(t, err)

	ctx := context.Background()

	require.NoError(t, service.ProcessReveal(ctx, secret, &assignment, currentHeight))
	require.NoError(t, service.ProcessReveal(ctx, secret, &assignment, currentHeight+1))

	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 1,
		"a wasted reveal costs more than a wasted accept and gets the same guard")
}

func TestInFlight_ConcurrentSubmissionsBroadcastOnce(t *testing.T) {
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)
	service := NewShareRevealService(cfg, mockClient, logger)

	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-10, currentHeight+20)
	assignment, err := mocks.CreateTestAssignmentWithRealEncryption(
		secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED", testKeypair.PublicKey)
	require.NoError(t, err)

	ctx := context.Background()

	// The poll loop and a block-header event can drive the same reveal at once,
	// and processReveals runs workers in parallel. Consulting the registry and
	// then submitting would let both callers read "clear"; the reservation is
	// atomic precisely so they cannot.
	const racers = 8
	var wg sync.WaitGroup
	for range racers {
		wg.Go(func() {
			_ = service.ProcessReveal(ctx, secret, &assignment, currentHeight)
		})
	}
	wg.Wait()

	assert.Len(t, mockClient.GetGuardianRevealShareCalls(), 1,
		"concurrent callers must produce exactly one broadcast")
}

func TestInFlight_RetainDropsReconciledEntries(t *testing.T) {
	registry := NewInFlightRegistry()

	require.True(t, registry.Reserve("secret-a", SubmissionConfirm, 100))
	require.True(t, registry.Reserve("secret-a", SubmissionReveal, 100))
	require.True(t, registry.Reserve("secret-b", SubmissionConfirm, 100))
	require.Equal(t, 3, registry.Len())

	// Reconciling frees the slot early: waiting out the expiry for work the
	// chain has already recorded would delay a legitimate later submission.
	registry.Retain(func(secretID string, kind SubmissionKind) bool {
		return !(secretID == "secret-a" && kind == SubmissionConfirm)
	})

	require.Equal(t, 2, registry.Len())
	assert.True(t, registry.Reserve("secret-a", SubmissionConfirm, 101),
		"a reconciled submission should no longer be treated as outstanding")
	assert.False(t, registry.Reserve("secret-a", SubmissionReveal, 101),
		"an unreconciled submission of another kind should still be held")
}
