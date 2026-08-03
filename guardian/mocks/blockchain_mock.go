package mocks

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/timeflareio/guardian/blockchain"
	"go.uber.org/zap"
)

// Ensure MockClient implements blockchain.ClientInterface
var _ blockchain.ClientInterface = (*MockClient)(nil)

// MockChain simulates blockchain state and block progression
type MockChain struct {
	currentHeight  int64
	secrets        map[string]*blockchain.Secret
	guardians      map[string]*blockchain.Guardian
	keyHistories   map[string][]blockchain.KeyEpoch
	blockCallbacks []func(height int64)
	mu             sync.RWMutex
}

// NewMockChain creates a new mock blockchain
func NewMockChain() *MockChain {
	return &MockChain{
		currentHeight:  1,
		secrets:        make(map[string]*blockchain.Secret),
		guardians:      make(map[string]*blockchain.Guardian),
		keyHistories:   make(map[string][]blockchain.KeyEpoch),
		blockCallbacks: make([]func(height int64), 0),
	}
}

// SetKeyHistory sets a guardian's key-epoch history (epoch order).
func (m *MockChain) SetKeyHistory(address string, history []blockchain.KeyEpoch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyHistories[address] = history
}

// GetKeyHistory returns a guardian's key-epoch history (may be empty).
func (m *MockChain) GetKeyHistory(address string) []blockchain.KeyEpoch {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keyHistories[address]
}

// GetCurrentHeight returns the current block height
func (m *MockChain) GetCurrentHeight() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentHeight
}

// AdvanceBlocks advances the blockchain by the specified number of blocks
func (m *MockChain) AdvanceBlocks(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := 0; i < count; i++ {
		m.currentHeight++
		// Execute callbacks for each block
		for _, callback := range m.blockCallbacks {
			callback(m.currentHeight)
		}
	}
}

// AdvanceToHeight advances the blockchain to a specific height
func (m *MockChain) AdvanceToHeight(height int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for m.currentHeight < height {
		m.currentHeight++
		for _, callback := range m.blockCallbacks {
			callback(m.currentHeight)
		}
	}
}

// OnBlockAdvance registers a callback for block advancement
func (m *MockChain) OnBlockAdvance(callback func(int64)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockCallbacks = append(m.blockCallbacks, callback)
}

// AddSecret adds a secret to the mock blockchain
func (m *MockChain) AddSecret(secret *blockchain.Secret) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[secret.ID] = secret
}

// GetSecret retrieves a secret by ID
func (m *MockChain) GetSecret(secretID string) (*blockchain.Secret, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	secret, exists := m.secrets[secretID]
	return secret, exists
}

// SetSecretState updates a secret's state
func (m *MockChain) SetSecretState(secretID, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if secret, exists := m.secrets[secretID]; exists {
		secret.State = state
	}
}

// AddGuardian adds a guardian to the mock blockchain
func (m *MockChain) AddGuardian(guardian *blockchain.Guardian) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guardians[guardian.Address] = guardian
}

// GetGuardian retrieves a guardian by address
func (m *MockChain) GetGuardian(address string) (*blockchain.Guardian, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	guardian, exists := m.guardians[address]
	return guardian, exists
}

// AddAssignment adds a guardian assignment to a secret
func (m *MockChain) AddAssignment(secretID, guardianAddr string, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if secret, exists := m.secrets[secretID]; exists {
		assignment := CreateTestAssignment(guardianAddr, status)
		secret.GuardianAssignments = append(secret.GuardianAssignments, assignment)
	}
}

// AddAssignmentWithRealEncryption adds a guardian assignment with real encryption for integration tests
func (m *MockChain) AddAssignmentWithRealEncryption(secretID, guardianAddr string, status string, guardianPublicKey [32]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if secret, exists := m.secrets[secretID]; exists {
		assignment, err := CreateTestAssignmentWithRealEncryption(secretID, guardianAddr, status, guardianPublicKey)
		if err != nil {
			return fmt.Errorf("failed to create assignment with real encryption: %w", err)
		}
		secret.GuardianAssignments = append(secret.GuardianAssignments, assignment)
	}
	return nil
}

// UpdateAssignmentStatus updates the status of a guardian assignment
func (m *MockChain) UpdateAssignmentStatus(secretID, guardianAddr, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if secret, exists := m.secrets[secretID]; exists {
		for i := range secret.GuardianAssignments {
			if secret.GuardianAssignments[i].GuardianAddress == guardianAddr {
				secret.GuardianAssignments[i].Status = status
				break
			}
		}
	}
}

// GetSecretsForGuardian returns all secrets assigned to a guardian
func (m *MockChain) GetSecretsForGuardian(guardianAddr string) []*blockchain.Secret {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*blockchain.Secret
	for _, secret := range m.secrets {
		for _, assignment := range secret.GuardianAssignments {
			if assignment.GuardianAddress == guardianAddr {
				result = append(result, secret)
				break
			}
		}
	}
	return result
}

// MockClient implements blockchain.Client for testing
type MockClient struct {
	chain  *MockChain
	logger *zap.Logger

	// Transaction simulation
	confirmSharesCalls []GuardianConfirmSharesCall
	revealShareCalls   []GuardianRevealShareCall

	// Failure simulation
	pingError          error
	getGuardianError   error
	getSecretError     error
	getHeightError     error
	listSecretsError   error
	confirmSharesError error
	revealShareError   error
}

// GuardianConfirmSharesCall represents a call to GuardianConfirmShares
type GuardianConfirmSharesCall struct {
	SecretID string
	Accept   bool
}

// GuardianRevealShareCall represents a call to GuardianRevealShare
type GuardianRevealShareCall struct {
	SecretID string
	Share    []byte
}

// NewMockClient creates a new mock blockchain client
func NewMockClient(chain *MockChain, logger *zap.Logger) *MockClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MockClient{
		chain:              chain,
		logger:             logger,
		confirmSharesCalls: make([]GuardianConfirmSharesCall, 0),
		revealShareCalls:   make([]GuardianRevealShareCall, 0),
	}
}

// SetPingError simulates chain connectivity failure
func (m *MockClient) SetPingError(err error) {
	m.pingError = err
}

// SetGetGuardianError simulates GetGuardian failure
func (m *MockClient) SetGetGuardianError(err error) {
	m.getGuardianError = err
}

// SetListSecretsError simulates ListSecretsForGuardian failure
func (m *MockClient) SetListSecretsError(err error) {
	m.listSecretsError = err
}

// SetGetHeightError simulates GetCurrentBlockHeight failure
func (m *MockClient) SetGetHeightError(err error) {
	m.getHeightError = err
}

// SetGuardianConfirmSharesError simulates GuardianConfirmShares failure
func (m *MockClient) SetGuardianConfirmSharesError(err error) {
	m.confirmSharesError = err
}

// SetGuardianRevealShareError simulates GuardianRevealShare failure
func (m *MockClient) SetGuardianRevealShareError(err error) {
	m.revealShareError = err
}

// GetGuardianConfirmSharesCalls returns all GuardianConfirmShares calls made
func (m *MockClient) GetGuardianConfirmSharesCalls() []GuardianConfirmSharesCall {
	return m.confirmSharesCalls
}

// GetGuardianRevealShareCalls returns all GuardianRevealShare calls made
func (m *MockClient) GetGuardianRevealShareCalls() []GuardianRevealShareCall {
	return m.revealShareCalls
}

// ClearCalls clears all recorded calls
func (m *MockClient) ClearCalls() {
	m.confirmSharesCalls = make([]GuardianConfirmSharesCall, 0)
	m.revealShareCalls = make([]GuardianRevealShareCall, 0)
}

// Ping implements blockchain.ClientInterface
func (m *MockClient) Ping(ctx context.Context) error {
	if m.pingError != nil {
		return m.pingError
	}
	return nil
}

// Close implements blockchain.ClientInterface
func (m *MockClient) Close() error {
	return nil
}

// SignerAddress implements blockchain.ClientInterface
func (m *MockClient) SignerAddress() string {
	return "tmflr1mocksigner"
}

// GetGuardian implements blockchain.Client
func (m *MockClient) GetGuardian(ctx context.Context, address string) (*blockchain.Guardian, error) {
	if m.getGuardianError != nil {
		return nil, m.getGuardianError
	}

	guardian, exists := m.chain.GetGuardian(address)
	if !exists {
		return nil, fmt.Errorf("guardian not found")
	}

	// Return a copy to avoid mutation
	guardianCopy := *guardian
	return &guardianCopy, nil
}

// GetSecret implements blockchain.Client
func (m *MockClient) GetSecret(ctx context.Context, secretID string) (*blockchain.Secret, error) {
	if m.getSecretError != nil {
		return nil, m.getSecretError
	}

	secret, exists := m.chain.GetSecret(secretID)
	if !exists {
		return nil, fmt.Errorf("secret not found")
	}

	// Return a copy to avoid mutation
	secretCopy := *secret
	return &secretCopy, nil
}

// ListSecretsForGuardian implements blockchain.Client
func (m *MockClient) ListSecretsForGuardian(ctx context.Context, guardianAddress string) ([]blockchain.Secret, error) {
	if m.listSecretsError != nil {
		return nil, m.listSecretsError
	}

	secrets := m.chain.GetSecretsForGuardian(guardianAddress)

	// Convert to slice of values (not pointers) to match interface
	result := make([]blockchain.Secret, len(secrets))
	for i, secret := range secrets {
		result[i] = *secret
	}

	return result, nil
}

// GetCurrentBlockHeight implements blockchain.Client
func (m *MockClient) GetCurrentBlockHeight(ctx context.Context) (int64, error) {
	if m.getHeightError != nil {
		return 0, m.getHeightError
	}
	return m.chain.GetCurrentHeight(), nil
}

// GetGuardianAddress implements blockchain.Client
func (m *MockClient) GetGuardianAddress(ctx context.Context, keyName string) (string, error) {
	// For testing, return a predictable address based on key name
	return fmt.Sprintf("tmflr1%s", keyName), nil
}

// GetBalance implements blockchain.Client
func (m *MockClient) GetBalance(ctx context.Context, address, denom string) (*blockchain.Balance, error) {
	return &blockchain.Balance{
		Denom:  denom,
		Amount: "1000000",
	}, nil
}

// GuardianConfirmShares implements blockchain.Client
func (m *MockClient) GuardianConfirmShares(ctx context.Context, secretID string, accept bool) (string, error) {
	if m.confirmSharesError != nil {
		return "", m.confirmSharesError
	}

	// Record the call
	m.confirmSharesCalls = append(m.confirmSharesCalls, GuardianConfirmSharesCall{
		SecretID: secretID,
		Accept:   accept,
	})

	// Simulate state change in mock chain
	// Since we no longer have share indices, this would be handled differently in the actual implementation
	// For mock purposes, we'll just update the first assignment or all assignments based on the caller's context
	if secret, exists := m.chain.GetSecret(secretID); exists {
		if len(secret.GuardianAssignments) > 0 {
			// For mock testing, update the first assignment
			status := "ASSIGNMENT_STATUS_REJECTED"
			if accept {
				status = "ASSIGNMENT_STATUS_ACCEPTED"
			}
			m.chain.UpdateAssignmentStatus(secretID, secret.GuardianAssignments[0].GuardianAddress, status)

			// Check if all assignments are accepted to move to pending state
			if accept {
				allAccepted := true
				for _, assignment := range secret.GuardianAssignments {
					if assignment.Status != "ASSIGNMENT_STATUS_ACCEPTED" {
						allAccepted = false
						break
					}
				}
				if allAccepted {
					m.chain.SetSecretState(secretID, "pending")
				}
			}
		}
	}

	return fmt.Sprintf("tx_confirm_%s_%d", secretID, time.Now().UnixNano()), nil
}

// GuardianRevealShare implements blockchain.Client
func (m *MockClient) GuardianRevealShare(ctx context.Context, secretID string, share []byte) (string, error) {
	if m.revealShareError != nil {
		return "", m.revealShareError
	}

	// Record the call
	m.revealShareCalls = append(m.revealShareCalls, GuardianRevealShareCall{
		SecretID: secretID,
		Share:    share,
	})

	// Simulate checking if threshold is met and updating secret state
	if secret, exists := m.chain.GetSecret(secretID); exists {
		revealedCount := int64(len(m.revealShareCalls))
		if revealedCount >= secret.Threshold {
			m.chain.SetSecretState(secretID, "revealed")
		}
	}

	return fmt.Sprintf("tx_reveal_%s_%d", secretID, time.Now().UnixNano()), nil
}

// GuardianRegister implements blockchain.ClientInterface
func (m *MockClient) GuardianRegister(ctx context.Context, opts blockchain.GuardianRegisterOptions) (string, error) {
	// For testing, just return a mock transaction hash
	return fmt.Sprintf("tx_register_%d", time.Now().UnixNano()), nil
}

// GuardianUpdate implements blockchain.ClientInterface
func (m *MockClient) GuardianUpdate(ctx context.Context, opts blockchain.GuardianUpdateOptions) (string, error) {
	return fmt.Sprintf("tx_update_%d", time.Now().UnixNano()), nil
}

// GetGuardianKeyHistory implements blockchain.ClientInterface. An empty
// history models pre-rotation state — resolvers treat it as single-epoch.
func (m *MockClient) GetGuardianKeyHistory(ctx context.Context, address string) ([]blockchain.KeyEpoch, error) {
	if m.getGuardianError != nil {
		return nil, m.getGuardianError
	}
	return m.chain.GetKeyHistory(address), nil
}

// GuardianRotateKey implements blockchain.ClientInterface: appends the next
// epoch (effective from the next block) to the mock chain's history and
// advances the guardian record, mirroring the chain's forward-only rule.
func (m *MockClient) GuardianRotateKey(ctx context.Context, newKey []byte) (string, error) {
	m.chain.mu.Lock()
	defer m.chain.mu.Unlock()

	address := m.SignerAddress()
	guardian, exists := m.chain.guardians[address]
	if !exists {
		return "", fmt.Errorf("guardian not found")
	}
	newEpoch := guardian.CurrentKeyEpoch + 1
	m.chain.keyHistories[address] = append(m.chain.keyHistories[address], blockchain.KeyEpoch{
		Epoch:               newEpoch,
		PublicKey:           newKey,
		EffectiveFromHeight: m.chain.currentHeight + 1,
	})
	guardian.CurrentKeyEpoch = newEpoch
	guardian.EncryptionPublicKey = newKey

	return fmt.Sprintf("tx_rotate_key_%d", time.Now().UnixNano()), nil
}
