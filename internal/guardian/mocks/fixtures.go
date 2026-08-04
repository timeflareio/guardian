package mocks

import (
	"fmt"

	"github.com/google/uuid"
	crypto "github.com/timeflareio/crypto/go"
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
	"go.uber.org/zap"
)

// TestGuardianAddress is a standard test guardian address
const TestGuardianAddress = "tmflr1testguardian123456789"

// TestSecretID is a standard test secret ID in UUID format
const TestSecretID = "12345678-1234-4234-8234-123456789012"

// GenerateTestSecretID generates a valid UUID format secret ID for testing
func GenerateTestSecretID() string {
	return uuid.New().String()
}

// GenerateDeterministicSecretID generates a deterministic UUID v5 for testing
// This ensures the same name always produces the same UUID, useful for tests
// that need predictable identifiers
func GenerateDeterministicSecretID(name string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)).String()
}

// CreateTestConfig creates a test guardian configuration
func CreateTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.GuardianID = "test-guardian"
	cfg.GuardianAddress = TestGuardianAddress
	cfg.MonitorName = "Test Guardian"
	cfg.KeyringBackend = "test"
	cfg.KeyringDir = "/tmp/test-keyring"
	cfg.KeyName = "test-key"
	cfg.EncryptionPrivateKeyPath = "/tmp/test-private-key"
	cfg.LogLevel = "debug"
	cfg.MetricsPort = 9100
	return cfg
}

// CreateTestGuardian creates a test guardian. Availability heights are block
// heights (the mock chain starts at height 1 and tests advance it).
func CreateTestGuardian(address string, available bool) *chain.Guardian {
	return &chain.Guardian{
		Address:             address,
		EncryptionPublicKey: []byte("0123456789abcdef0123456789abcdef"), // 32 bytes
		AvailableFrom:       1,
		AvailableUntil:      1_000_000,
		Stake: chain.Coin{
			Denom:  "uveil",
			Amount: "10000000000", // 10,000 VEIL
		},
		AcceptingSecrets: available,
	}
}

// CreateTestGuardianWithRealKeypair creates a test guardian with a real keypair for integration tests
func CreateTestGuardianWithRealKeypair(address string, available bool) (*chain.Guardian, [32]byte, error) {
	// Generate a real keypair for integration testing
	keypair, err := crypto.GenerateKeypair()
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("failed to generate keypair: %w", err)
	}

	guardian := CreateTestGuardian(address, available)
	guardian.EncryptionPublicKey = keypair.PublicKey[:]

	return guardian, keypair.PrivateKey, nil
}

// CreateTestSecret creates a test secret
func CreateTestSecret(id string, threshold int64, state string) *chain.Secret {
	return &chain.Secret{
		ID:                  id,
		Creator:             "tmflr1creator123456789",
		State:               state,
		Threshold:           threshold,
		GuardianAssignments: make([]chain.GuardianAssignment, 0),
		RevealStartBlock:    100,
		RevealEndBlock:      200,
	}
}

// CreateTestSecretWithRevealWindow creates a test secret with specific reveal window
func CreateTestSecretWithRevealWindow(id string, threshold int64, state string, revealStart, revealEnd int64) *chain.Secret {
	secret := CreateTestSecret(id, threshold, state)
	secret.RevealStartBlock = revealStart
	secret.RevealEndBlock = revealEnd
	return secret
}

// CreateTestAssignment creates a test guardian assignment for unit tests
func CreateTestAssignment(guardianAddr string, status string) chain.GuardianAssignment {
	return chain.GuardianAssignment{
		GuardianAddress: guardianAddr,
		EncryptedShare:  []byte(fmt.Sprintf("test_share_data_%s", guardianAddr)),
		ShareHMAC:       []byte(fmt.Sprintf("test_hmac_%s", guardianAddr)),
		Status:          status,
	}
}

// CreateTestAssignmentWithRealEncryption creates a test guardian assignment with properly encrypted data and real HMAC for integration tests
func CreateTestAssignmentWithRealEncryption(secretID, guardianAddr string, status string, guardianPublicKey [32]byte) (chain.GuardianAssignment, error) {
	// Create test share data
	shareData := fmt.Sprintf("test_share_data_%s", guardianAddr)

	// Properly encrypt the share data
	encryptedShare, err := CreateProperlyEncryptedShareForTesting([]byte(shareData), guardianPublicKey)
	if err != nil {
		return chain.GuardianAssignment{}, fmt.Errorf("failed to encrypt test share: %w", err)
	}

	// Generate real HMAC using crypto library
	hmacBytes, err := crypto.GenerateHMAC(secretID, guardianAddr, []byte(shareData))
	if err != nil {
		return chain.GuardianAssignment{}, fmt.Errorf("failed to generate HMAC: %w", err)
	}

	return chain.GuardianAssignment{
		GuardianAddress: guardianAddr,
		EncryptedShare:  encryptedShare,
		ShareHMAC:       hmacBytes,
		Status:          status,
	}, nil
}

// SetupTestScenario creates a complete test scenario with guardians and secrets
func SetupTestScenario(chain *MockChain) {
	// Add test guardian
	guardian := CreateTestGuardian(TestGuardianAddress, true)
	chain.AddGuardian(guardian)

	// Add additional guardians for multi-guardian scenarios
	for i := 1; i <= 5; i++ {
		addr := fmt.Sprintf("tmflr1guardian%d", i)
		g := CreateTestGuardian(addr, true)
		chain.AddGuardian(g)
	}

	// Create secrets in different states with deterministic IDs for predictable testing
	secretAwaitingAcceptance := CreateTestSecret(GenerateDeterministicSecretID("test-awaiting-acceptance"), 3, "awaiting_acceptance")
	chain.AddSecret(secretAwaitingAcceptance)
	chain.AddAssignment(secretAwaitingAcceptance.ID, TestGuardianAddress, "ASSIGNMENT_STATUS_PROPOSED")

	secretPending := CreateTestSecret(GenerateDeterministicSecretID("test-pending"), 3, "pending")
	chain.AddSecret(secretPending)
	chain.AddAssignment(secretPending.ID, TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")

	secretRevealed := CreateTestSecret(GenerateDeterministicSecretID("test-revealed"), 3, "revealed")
	chain.AddSecret(secretRevealed)
	chain.AddAssignment(secretRevealed.ID, TestGuardianAddress, "ASSIGNMENT_STATUS_ACCEPTED")
}

// SetupMultiGuardianSecret creates a secret with multiple guardian assignments
// Returns the generated secret ID for use in tests
func SetupMultiGuardianSecret(chain *MockChain, threshold int64, guardianAddresses []string, state string) string {
	secretID := GenerateTestSecretID()
	secret := CreateTestSecret(secretID, threshold, state)
	chain.AddSecret(secret)

	for _, addr := range guardianAddresses {
		status := "ASSIGNMENT_STATUS_PROPOSED"
		if state == "pending" {
			status = "ASSIGNMENT_STATUS_ACCEPTED"
		}
		chain.AddAssignment(secret.ID, addr, status)
	}
	return secretID
}

// CreateTestLogger creates a test logger
func CreateTestLogger() *zap.Logger {
	config := zap.NewDevelopmentConfig()
	config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	logger, _ := config.Build()
	return logger
}

// AdvanceToRevealWindow advances the chain to when the secret can be revealed
func AdvanceToRevealWindow(chain *MockChain, secretID string) {
	secret, exists := chain.GetSecret(secretID)
	if !exists {
		return
	}

	if chain.GetCurrentHeight() < secret.RevealStartBlock {
		chain.AdvanceToHeight(secret.RevealStartBlock)
	}
}

// AdvancePastRevealWindow advances the chain past the reveal window
func AdvancePastRevealWindow(chain *MockChain, secretID string) {
	secret, exists := chain.GetSecret(secretID)
	if !exists {
		return
	}

	chain.AdvanceToHeight(secret.RevealEndBlock + 10)
}

// CreateSecretLifecycleScenario creates a complete secret lifecycle scenario
func CreateSecretLifecycleScenario(chain *MockChain, secretID string, threshold int64) {
	// Create guardians
	guardianAddresses := make([]string, threshold+2) // Extra guardians for redundancy
	for i := int64(0); i < threshold+2; i++ {
		addr := fmt.Sprintf("tmflr1guardian%d", i)
		guardian := CreateTestGuardian(addr, true)
		chain.AddGuardian(guardian)
		guardianAddresses[i] = addr
	}

	// Create secret in awaiting_acceptance state
	secret := CreateTestSecret(secretID, threshold, "awaiting_acceptance")
	chain.AddSecret(secret)

	for _, addr := range guardianAddresses {
		status := "ASSIGNMENT_STATUS_PROPOSED"
		chain.AddAssignment(secret.ID, addr, status)
	}
}

// SimulateSecretAcceptance simulates guardians accepting their assignments
func SimulateSecretAcceptance(chain *MockChain, secretID string, acceptingGuardians []string) {
	secret, exists := chain.GetSecret(secretID)
	if !exists {
		return
	}

	// Update assignment statuses
	for _, guardianAddr := range acceptingGuardians {
		chain.UpdateAssignmentStatus(secretID, guardianAddr, "ASSIGNMENT_STATUS_ACCEPTED")
	}

	// Check if we have enough acceptances to move to pending
	acceptedCount := int64(0)
	for _, assignment := range secret.GuardianAssignments {
		if assignment.Status == "ASSIGNMENT_STATUS_ACCEPTED" {
			acceptedCount++
		}
	}

	if acceptedCount >= secret.Threshold {
		chain.SetSecretState(secretID, "pending")
	}
}

// SimulateSecretReveals simulates guardians revealing their shares
func SimulateSecretReveals(chain *MockChain, secretID string, revealingGuardians []string) {
	secret, exists := chain.GetSecret(secretID)
	if !exists {
		return
	}

	// For simulation purposes, we'll track reveals in guardian assignments
	if int64(len(revealingGuardians)) >= secret.Threshold {
		chain.SetSecretState(secretID, "revealed")
	}
}
