package guardian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/guardian/mocks"
)

// testConfirmHeight is the height confirmation tests submit at. Each test
// builds a fresh service, so one height per test is enough — the in-flight
// guard is exercised deliberately in inflight_test.go rather than incidentally
// here.
const testConfirmHeight = int64(100)

// TestKeypair holds both private and public keys for testing
type TestKeypair struct {
	PrivateKeyPath string
	PrivateKey     [32]byte
	PublicKey      [32]byte
}

func setupTestCrypto(t *testing.T) (*TestKeypair, func()) {
	// Create temporary directory for test crypto files
	tmpDir := t.TempDir()
	privateKeyPath := filepath.Join(tmpDir, "private_key")

	// Generate a real keypair for testing
	keypair, err := crypto.GenerateKeypair()
	require.NoError(t, err)

	// Write the private key to file
	err = os.WriteFile(privateKeyPath, keypair.PrivateKey[:], 0600)
	require.NoError(t, err)

	return &TestKeypair{
			PrivateKeyPath: privateKeyPath,
			PrivateKey:     keypair.PrivateKey,
			PublicKey:      keypair.PublicKey,
		}, func() {
			// Cleanup is handled by t.TempDir()
		}
}

func TestNewShareRevealService(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	assert.NotNil(t, service)
	assert.Equal(t, cfg, service.config)
	assert.Equal(t, mockClient, service.client)
	assert.Equal(t, logger, service.logger)
	assert.Equal(t, mocks.TestGuardianAddress, service.config.GuardianAddress)
}

func TestProcessConfirmation_Success(t *testing.T) {
	// Setup crypto files with real keypair
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create test secret and assignment
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")

	// Create properly encrypted share using the test keypair
	testShareData := []byte("test share data")
	encryptedShare, err := mocks.CreateProperlyEncryptedShareForTesting(testShareData, testKeypair.PublicKey)
	require.NoError(t, err)
	assignment.EncryptedShare = encryptedShare

	// Generate proper HMAC using the crypto library
	hmacBytes, err := crypto.GenerateHMAC(secret.ID, mocks.TestGuardianAddress, testShareData)
	require.NoError(t, err)
	assignment.ShareHMAC = hmacBytes

	ctx := context.Background()

	// Test successful confirmation
	err = service.ProcessConfirmation(ctx, secret, &assignment, testConfirmHeight)

	// Verify
	require.NoError(t, err)

	// Check that confirm transaction was called
	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	assert.Equal(t, secret.ID, confirmCalls[0].SecretID)
	// ShareIndices field removed - share indices now embedded in share data
	assert.True(t, confirmCalls[0].Accept)
}

func TestProcessConfirmation_EmptyEncryptedShare(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create test secret and assignment with empty encrypted share
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")
	assignment.EncryptedShare = nil // Empty share

	ctx := context.Background()

	// Test
	err := service.ProcessConfirmation(ctx, secret, &assignment, testConfirmHeight)

	// Should fail but gracefully reject
	require.NoError(t, err)

	// Check that reject transaction was called
	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	assert.Equal(t, secret.ID, confirmCalls[0].SecretID)
	assert.False(t, confirmCalls[0].Accept)
}

func TestProcessConfirmation_GuardianAddressNotSet(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)
	// Don't set guardian address

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")
	assignment.EncryptedShare = []byte("test data")

	ctx := context.Background()

	// Test
	err := service.ProcessConfirmation(ctx, secret, &assignment, testConfirmHeight)

	// Should reject due to missing guardian address
	require.NoError(t, err)

	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	assert.False(t, confirmCalls[0].Accept)
}

func TestProcessConfirmation_DecryptionFailure(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = "/nonexistent/path" // Will cause decryption failure
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")
	assignment.EncryptedShare = []byte("test data")

	ctx := context.Background()

	// Test
	err := service.ProcessConfirmation(ctx, secret, &assignment, testConfirmHeight)

	// Should reject due to decryption failure
	require.NoError(t, err)

	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	assert.False(t, confirmCalls[0].Accept)
}

func TestProcessConfirmation_HMACValidation(t *testing.T) {
	// Setup crypto files
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
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")
	assignment.EncryptedShare = []byte("test data")
	assignment.ShareHMAC = []byte("invalid hmac")

	ctx := context.Background()

	// Test - HMAC validation will fail with current setup
	err := service.ProcessConfirmation(ctx, secret, &assignment, testConfirmHeight)

	// Should complete (either accept or reject based on HMAC validation)
	require.NoError(t, err)

	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	// In this case, will likely reject due to HMAC mismatch
}

func TestProcessConfirmation_RejectsAnInvalidHMAC(t *testing.T) {
	// A share whose HMAC does not verify is rejected, always. The chain checks
	// the same HMAC when it accepts the reveal, so accepting here would commit a
	// bond to a share that is slashable on submission.
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
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")

	testShareData := []byte("test share data")
	encryptedShare, err := mocks.CreateProperlyEncryptedShareForTesting(testShareData, testKeypair.PublicKey)
	require.NoError(t, err)
	assignment.EncryptedShare = encryptedShare
	assignment.ShareHMAC = []byte("invalid_hmac")

	err = service.ProcessConfirmation(context.Background(), secret, &assignment, testConfirmHeight)
	require.NoError(t, err)

	confirmCalls := mockClient.GetGuardianConfirmSharesCalls()
	require.Len(t, confirmCalls, 1)
	assert.False(t, confirmCalls[0].Accept, "a share with a bad HMAC must be rejected, not accepted")
}

func TestProcessReveal_Success(t *testing.T) {
	// Setup crypto files
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create secret in reveal window
	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-10, currentHeight+20)

	// Create assignment with real encryption using the test keypair
	assignment, err := mocks.CreateTestAssignmentWithRealEncryption(secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED", testKeypair.PublicKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Test
	err = service.ProcessReveal(ctx, secret, &assignment, currentHeight)

	// Verify
	require.NoError(t, err)

	// Check that reveal transaction was called
	revealCalls := mockClient.GetGuardianRevealShareCalls()
	require.Len(t, revealCalls, 1)
	assert.Equal(t, secret.ID, revealCalls[0].SecretID)
	// ShareIndex field removed - share index now embedded in decrypted share data
	assert.NotEmpty(t, revealCalls[0].Share)
}

func TestProcessReveal_TooEarly(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create secret with future reveal window
	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight+50, currentHeight+100)
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")

	ctx := context.Background()

	// Test
	err := service.ProcessReveal(ctx, secret, &assignment, currentHeight)

	// Should not error, but also not reveal
	require.NoError(t, err)

	// No reveal calls should be made
	revealCalls := mockClient.GetGuardianRevealShareCalls()
	assert.Len(t, revealCalls, 0)
}

func TestProcessReveal_TooLate(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create secret with past reveal window
	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-100, currentHeight-50)
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")

	ctx := context.Background()

	// Test
	err := service.ProcessReveal(ctx, secret, &assignment, currentHeight)

	// Should not error, but also not reveal
	require.NoError(t, err)

	// No reveal calls should be made
	revealCalls := mockClient.GetGuardianRevealShareCalls()
	assert.Len(t, revealCalls, 0)
}

func TestProcessReveal_DecryptionFailure(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = "/nonexistent/path" // Will cause decryption failure
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create secret in reveal window
	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-10, currentHeight+20)
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")
	assignment.EncryptedShare = []byte("test data")

	ctx := context.Background()

	// Test
	err := service.ProcessReveal(ctx, secret, &assignment, currentHeight)

	// Should error due to decryption failure
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decrypt share")

	// No reveal calls should be made
	revealCalls := mockClient.GetGuardianRevealShareCalls()
	assert.Len(t, revealCalls, 0)
}

func TestProcessReveal_TransactionFailure(t *testing.T) {
	// Setup crypto files
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Simulate transaction failure
	mockClient.SetGuardianRevealShareError(fmt.Errorf("transaction failed"))

	service := NewShareRevealService(cfg, mockClient, logger)

	// Create secret in reveal window
	currentHeight := mockChain.GetCurrentHeight()
	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecretWithRevealWindow(secretID, 3, "pending",
		currentHeight-10, currentHeight+20)

	// Create assignment with real encryption using the test keypair
	assignment, err := mocks.CreateTestAssignmentWithRealEncryption(secretID, mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED", testKeypair.PublicKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Test
	err = service.ProcessReveal(ctx, secret, &assignment, currentHeight)

	// Should error due to transaction failure
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reveal transaction failed")
}

func TestShouldRevealNow(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	currentHeight := int64(100)

	testCases := []struct {
		name         string
		revealStart  int64
		revealEnd    int64
		shouldReveal bool
	}{
		{
			name:         "Too early - before reveal start",
			revealStart:  110,
			revealEnd:    120,
			shouldReveal: false,
		},
		{
			name:         "Too early - before reveal window",
			revealStart:  101,
			revealEnd:    120,
			shouldReveal: false, // current is 100, reveal starts at 101
		},
		{
			name:         "Perfect timing - exactly at reveal start",
			revealStart:  100,
			revealEnd:    120,
			shouldReveal: true, // current is 100, reveal starts at 100
		},
		{
			name:         "In window - after reveal start",
			revealStart:  90,
			revealEnd:    120,
			shouldReveal: true, // current is 100, reveal started at 90
		},
		{
			name:         "Too late - after reveal end",
			revealStart:  80,
			revealEnd:    95,
			shouldReveal: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			secret := mocks.CreateTestSecretWithRevealWindow("test", 3, "pending",
				tc.revealStart, tc.revealEnd)

			result := service.shouldRevealNow(secret, currentHeight)
			assert.Equal(t, tc.shouldReveal, result)
		})
	}
}

func TestDecryptShare(t *testing.T) {
	// Setup crypto files
	testKeypair, cleanup := setupTestCrypto(t)
	defer cleanup()

	cfg := mocks.CreateTestConfig()
	cfg.EncryptionPrivateKeyPath = testKeypair.PrivateKeyPath
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	service := NewShareRevealService(cfg, mockClient, logger)

	// Test successful decryption with properly encrypted data
	ctx := context.Background()
	secret := mocks.CreateTestSecret(mocks.GenerateTestSecretID(), 3, "awaiting_acceptance")
	testData := []byte("test share data")
	encryptedShare, err := mocks.CreateProperlyEncryptedShareForTesting(testData, testKeypair.PublicKey)
	require.NoError(t, err)

	decrypted, err := service.decryptShare(ctx, secret, encryptedShare)
	require.NoError(t, err)
	assert.Equal(t, testData, decrypted)

	// Test empty encrypted share
	_, err = service.decryptShare(ctx, secret, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty encrypted share")

	// Test undecryptable ciphertext
	_, err = service.decryptShare(ctx, secret, []byte("not a real ciphertext"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decrypt share")
}

func TestConfirmationTransactionError(t *testing.T) {
	cfg := mocks.CreateTestConfig()
	logger := mocks.CreateTestLogger()
	mockChain := mocks.NewMockChain()
	mockClient := mocks.NewMockClient(mockChain, logger)

	// Simulate transaction failure
	mockClient.SetGuardianConfirmSharesError(fmt.Errorf("blockchain transaction failed"))

	service := NewShareRevealService(cfg, mockClient, logger)

	secretID := mocks.GenerateTestSecretID()
	secret := mocks.CreateTestSecret(secretID, 3, "awaiting_acceptance")
	assignment := mocks.CreateTestAssignment(mocks.TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")
	assignment.EncryptedShare = nil // Empty to trigger rejection

	ctx := context.Background()

	// Test
	err := service.ProcessConfirmation(ctx, secret, &assignment, testConfirmHeight)

	// Should error due to transaction failure
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blockchain transaction failed")
}
