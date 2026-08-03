package mocks

import (
	"encoding/base64"
	"fmt"

	crypto "github.com/timeflareio/crypto/go"
)

// MockCrypto provides mock implementations for cryptographic operations
type MockCrypto struct {
	// Control behavior
	decryptionShouldFail bool
	hmacShouldFail       bool
	hmacValidationFails  bool

	// Track calls
	decryptCalls []DecryptCall
	hmacCalls    []HMACCall
}

// DecryptCall represents a call to decrypt
type DecryptCall struct {
	EncryptedData []byte
	PrivateKey    [32]byte
}

// HMACCall represents a call to generate HMAC
type HMACCall struct {
	SecretID     string
	GuardianAddr string
	Share        []byte
}

// NewMockCrypto creates a new mock crypto instance
func NewMockCrypto() *MockCrypto {
	return &MockCrypto{
		decryptCalls: make([]DecryptCall, 0),
		hmacCalls:    make([]HMACCall, 0),
	}
}

// SetDecryptionShouldFail controls whether decryption operations should fail
func (m *MockCrypto) SetDecryptionShouldFail(shouldFail bool) {
	m.decryptionShouldFail = shouldFail
}

// SetHMACShouldFail controls whether HMAC generation should fail
func (m *MockCrypto) SetHMACShouldFail(shouldFail bool) {
	m.hmacShouldFail = shouldFail
}

// SetHMACValidationFails controls whether HMAC validation should fail
func (m *MockCrypto) SetHMACValidationFails(shouldFail bool) {
	m.hmacValidationFails = shouldFail
}

// GetDecryptCalls returns all decrypt calls made
func (m *MockCrypto) GetDecryptCalls() []DecryptCall {
	return m.decryptCalls
}

// GetHMACCalls returns all HMAC calls made
func (m *MockCrypto) GetHMACCalls() []HMACCall {
	return m.hmacCalls
}

// ClearCalls clears all recorded calls
func (m *MockCrypto) ClearCalls() {
	m.decryptCalls = make([]DecryptCall, 0)
	m.hmacCalls = make([]HMACCall, 0)
}

// MockDecryptShareWithPrivateKey simulates share decryption
func (m *MockCrypto) MockDecryptShareWithPrivateKey(encryptedData []byte, privateKey [32]byte) ([]byte, error) {
	// Record the call
	m.decryptCalls = append(m.decryptCalls, DecryptCall{
		EncryptedData: encryptedData,
		PrivateKey:    privateKey,
	})

	if m.decryptionShouldFail {
		return nil, fmt.Errorf("mock decryption failure")
	}

	// For testing, return a predictable decrypted share based on input
	// This allows tests to verify the decryption process without real crypto
	shareData := fmt.Sprintf("decrypted_share_%x", encryptedData[:min(8, len(encryptedData))])
	return []byte(shareData), nil
}

// MockGenerateHMAC simulates HMAC generation
func (m *MockCrypto) MockGenerateHMAC(secretID, guardianAddr string, share []byte) ([]byte, error) {
	// Record the call
	m.hmacCalls = append(m.hmacCalls, HMACCall{
		SecretID:     secretID,
		GuardianAddr: guardianAddr,
		Share:        share,
	})

	if m.hmacShouldFail {
		return nil, fmt.Errorf("mock HMAC generation failure")
	}

	// Generate predictable HMAC for testing
	hmacData := fmt.Sprintf("hmac_%s_%s_%x", secretID, guardianAddr, share[:min(4, len(share))])
	return []byte(hmacData), nil
}

// MockValidateHMAC simulates HMAC validation
func (m *MockCrypto) MockValidateHMAC(expectedHMAC, actualHMAC []byte) bool {
	if m.hmacValidationFails {
		return false
	}

	// For predictable testing, consider HMACs equal if they have the same length
	// In real tests, we'll use the MockGenerateGuardianHMAC to create expected values
	return len(expectedHMAC) == len(actualHMAC)
}

// CreateTestEncryptedShare creates a test encrypted share that works with the test environment
// This creates data that looks like proper encrypted shares for testing purposes
func CreateTestEncryptedShare(secretID string, shareIndex int64) string {
	// Create test share data that will decrypt to predictable content
	shareData := fmt.Sprintf("test_share_%s_%d", secretID, shareIndex)

	// Create fake encrypted data - in tests, this will be intercepted by our mock decrypt function
	// Format: [nonce(12 bytes)][ephemeral_public_key(32 bytes)][encrypted_data][tag(16 bytes)]
	// This mimics the real encryption format enough to pass basic validation
	fakeNonce := make([]byte, 12)
	fakeEphemeralKey := make([]byte, 32)
	fakeTag := make([]byte, 16)

	// Fill with predictable test data based on the secret and share
	for i := range fakeNonce {
		fakeNonce[i] = byte((i + int(shareIndex)) % 256)
	}
	for i := range fakeEphemeralKey {
		fakeEphemeralKey[i] = byte((i + len(secretID) + int(shareIndex)) % 256)
	}
	for i := range fakeTag {
		fakeTag[i] = byte((i + len(shareData)) % 256)
	}

	// Combine into fake encrypted format
	encryptedData := append(fakeNonce, fakeEphemeralKey...)
	encryptedData = append(encryptedData, []byte(shareData)...)
	encryptedData = append(encryptedData, fakeTag...)

	return base64.StdEncoding.EncodeToString(encryptedData)
}

// CreateProperlyEncryptedShareForTesting creates a test encrypted share using real encryption
// This encrypts the share data with the guardian's public key using the Rust crypto FFI
func CreateProperlyEncryptedShareForTesting(shareData []byte, guardianPublicKey [32]byte) ([]byte, error) {
	if len(shareData) == 0 {
		return nil, fmt.Errorf("share data cannot be empty")
	}

	// Use the real crypto library to encrypt the share; raw bytes, exactly as
	// the typed gRPC query serves them.
	encryptedData, err := crypto.EncryptShareWithPublicKey(shareData, guardianPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt test share: %w", err)
	}

	return encryptedData, nil
}

// CreateTestKeypairAndEncryptedShare creates a keypair and encrypted share for testing
// Returns the private key and the base64-encoded encrypted share
func CreateTestKeypairAndEncryptedShare(shareData []byte) (privateKey [32]byte, encryptedShare string, err error) {
	if len(shareData) == 0 {
		return privateKey, "", fmt.Errorf("share data cannot be empty")
	}

	// Generate keypair using basic function
	keypair, err := crypto.GenerateKeypair()
	if err != nil {
		return privateKey, "", fmt.Errorf("failed to generate keypair: %w", err)
	}

	// Encrypt data using basic function
	encryptedData, err := crypto.EncryptShareWithPublicKey(shareData, keypair.PublicKey)
	if err != nil {
		return privateKey, "", fmt.Errorf("failed to encrypt data: %w", err)
	}

	encryptedShare = base64.StdEncoding.EncodeToString(encryptedData)
	return keypair.PrivateKey, encryptedShare, nil
}

// CreateTestHMAC creates a test HMAC value
func CreateTestHMAC(secretID, guardianAddr string, shareIndex int64) string {
	hmacData := fmt.Sprintf("test_hmac_%s_%s_%d", secretID, guardianAddr, shareIndex)
	return fmt.Sprintf("%x", hmacData)
}

// CreateTestPrivateKey creates a test private key
func CreateTestPrivateKey() [32]byte {
	var key [32]byte
	// Fill with predictable test data
	for i := 0; i < 32; i++ {
		key[i] = byte(i + 1)
	}
	return key
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
