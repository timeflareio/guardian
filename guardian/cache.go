package guardian

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/timeflareio/guardian/blockchain"
	"go.uber.org/zap"
)

// SecretLocalState represents the processing state for a secret from guardian's perspective
type SecretLocalState int

const (
	StateNeedsConfirmation SecretLocalState = iota // awaiting_acceptance + PROPOSED
	StateNeedsReveal                               // pending + ACCEPTED + in reveal window
	StateRevealed                                  // We've revealed our share
	StateEvictable                                 // revealed/cancelled/failed - can evict
)

// String returns human-readable state name
func (s SecretLocalState) String() string {
	switch s {
	case StateNeedsConfirmation:
		return "needs_confirmation"
	case StateNeedsReveal:
		return "needs_reveal"
	case StateRevealed:
		return "revealed"
	case StateEvictable:
		return "evictable"
	default:
		return "unknown"
	}
}

// CachedSecret represents a secret with guardian-specific metadata
type CachedSecret struct {
	Secret      *blockchain.Secret             // Full secret data from blockchain
	Assignment  *blockchain.GuardianAssignment // Our specific assignment
	LocalState  SecretLocalState               // Our derived processing state
	LastUpdated int64                          // Block height when last updated
	AddedAt     time.Time                      // When added to cache (for TTL)
}

// ActiveSecretCache maintains an efficient cache of secrets requiring guardian action
type ActiveSecretCache struct {
	// Core cache storage
	secrets map[string]*CachedSecret

	// Indexed access for efficient state-based operations
	awaitingConfirmation map[string]*CachedSecret // StateNeedsConfirmation secrets
	pendingReveal        map[string]*CachedSecret // StateNeedsReveal secrets

	// Eviction and cleanup tracking
	lastCleanupBlock int64         // Last block height we performed cleanup
	cleanupInterval  int64         // How often to run cleanup (in blocks)
	maxCacheAge      time.Duration // Maximum age before eviction

	// Concurrency control
	mu sync.RWMutex

	// Logging
	logger *zap.Logger
}

// NewActiveSecretCache creates a new active secret cache. maxAge and
// cleanupIntervalBlocks come from configuration (cache_max_age,
// cache_cleanup_interval); zero values fall back to safe defaults.
func NewActiveSecretCache(logger *zap.Logger, maxAge time.Duration, cleanupIntervalBlocks int64) *ActiveSecretCache {
	if maxAge <= 0 {
		maxAge = 7 * 24 * time.Hour
	}
	if cleanupIntervalBlocks <= 0 {
		cleanupIntervalBlocks = 50
	}
	return &ActiveSecretCache{
		secrets:              make(map[string]*CachedSecret),
		awaitingConfirmation: make(map[string]*CachedSecret),
		pendingReveal:        make(map[string]*CachedSecret),
		cleanupInterval:      cleanupIntervalBlocks,
		maxCacheAge:          maxAge,
		logger:               logger,
	}
}

// Get retrieves a cached secret by ID
func (cache *ActiveSecretCache) Get(secretID string) *CachedSecret {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.secrets[secretID]
}

// GetAll returns all cached secrets (for debugging/monitoring)
func (cache *ActiveSecretCache) GetAll() map[string]*CachedSecret {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	result := make(map[string]*CachedSecret, len(cache.secrets))
	for id, secret := range cache.secrets {
		result[id] = secret
	}
	return result
}

// GetSecretsNeedingConfirmation returns all secrets awaiting confirmation
func (cache *ActiveSecretCache) GetSecretsNeedingConfirmation() []*CachedSecret {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	result := make([]*CachedSecret, 0, len(cache.awaitingConfirmation))
	for _, secret := range cache.awaitingConfirmation {
		result = append(result, secret)
	}
	return result
}

// GetSecretsNeedingReveal returns secrets ready for reveal at current block height
func (cache *ActiveSecretCache) GetSecretsNeedingReveal(currentHeight int64) []*CachedSecret {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	result := make([]*CachedSecret, 0)
	for _, secret := range cache.pendingReveal {
		if cache.isInRevealWindow(secret.Secret, currentHeight) {
			result = append(result, secret)
		}
	}
	return result
}

// Size returns the total number of cached secrets
func (cache *ActiveSecretCache) Size() int {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return len(cache.secrets)
}

// GetStateCount returns count of secrets in each state
func (cache *ActiveSecretCache) GetStateCount() map[SecretLocalState]int {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	counts := make(map[SecretLocalState]int)
	for _, secret := range cache.secrets {
		counts[secret.LocalState]++
	}
	return counts
}

// isActionableSecret determines if a secret requires any guardian action
func (cache *ActiveSecretCache) isActionableSecret(secret *blockchain.Secret, guardianAddr string, currentHeight int64) bool {
	// Find our assignment
	assignment := cache.findAssignment(secret, guardianAddr)
	if assignment == nil {
		return false // Not assigned to us
	}

	// Determine state and check if actionable
	state := cache.determineLocalState(secret, assignment, currentHeight)
	return state == StateNeedsConfirmation || state == StateNeedsReveal
}

// findAssignment finds our guardian assignment in a secret
func (cache *ActiveSecretCache) findAssignment(secret *blockchain.Secret, guardianAddr string) *blockchain.GuardianAssignment {
	for i := range secret.GuardianAssignments {
		if secret.GuardianAssignments[i].GuardianAddress == guardianAddr {
			return &secret.GuardianAssignments[i]
		}
	}
	return nil
}

// determineLocalState determines the processing state for a secret
func (cache *ActiveSecretCache) determineLocalState(secret *blockchain.Secret, assignment *blockchain.GuardianAssignment, currentHeight int64) SecretLocalState {
	// Check for final states first
	switch secret.State {
	case "revealed", "cancelled", "failed":
		return StateEvictable
	}

	// Check if we need to confirm
	if secret.State == "awaiting_acceptance" && assignment.Status == "ASSIGNMENT_STATUS_PROPOSED" {
		return StateNeedsConfirmation
	}

	// Check if we need to reveal. CRITICAL: "reconstructable" (threshold met,
	// window still open) counts — the chain accepts reveals in both states, and
	// every accepted guardian that fails to reveal before the window closes is
	// slashed at settlement regardless of whether the threshold was already
	// met. Treating reconstructable as evictable silently no-showed the
	// slowest-polling guardian and cost it 50% of its bond.
	if (secret.State == "pending" || secret.State == "reconstructable") &&
		assignment.Status == "ASSIGNMENT_STATUS_ACCEPTED" {
		if secret.HasRevealed(assignment.GuardianAddress) {
			return StateEvictable // our share is on-chain; nothing left to do
		}
		return StateNeedsReveal
	}

	// Default to evictable for any other states
	return StateEvictable
}

// isInRevealWindow checks if current height is within reveal window
func (cache *ActiveSecretCache) isInRevealWindow(secret *blockchain.Secret, currentHeight int64) bool {
	if currentHeight < secret.RevealStartBlock {
		return false // Too early
	}
	// The chain's window is [start, end] — BOTH bounds inclusive. A reveal in
	// block end is valid; settlement runs in the EndBlock of block end + 1.
	return currentHeight <= secret.RevealEndBlock
}

// shouldEvict determines if a secret should be evicted from cache
func (cache *ActiveSecretCache) shouldEvict(cached *CachedSecret, currentHeight int64) bool {
	// Evict if in evictable state or revealed state
	if cached.LocalState == StateEvictable || cached.LocalState == StateRevealed {
		return true
	}

	// Evict if too old (TTL)
	if time.Since(cached.AddedAt) > cache.maxCacheAge {
		return true
	}

	// Evict if reveal window has passed and not revealed
	if cached.Secret.RevealEndBlock > 0 && currentHeight > cached.Secret.RevealEndBlock {
		return true
	}

	return false
}

// addToIndex adds a secret to the appropriate state index
func (cache *ActiveSecretCache) addToIndex(secretID string, cached *CachedSecret) {
	switch cached.LocalState {
	case StateNeedsConfirmation:
		cache.awaitingConfirmation[secretID] = cached
	case StateNeedsReveal:
		cache.pendingReveal[secretID] = cached
	}
}

// removeFromIndex removes a secret from all state indices
func (cache *ActiveSecretCache) removeFromIndex(secretID string) {
	delete(cache.awaitingConfirmation, secretID)
	delete(cache.pendingReveal, secretID)
}

// Initialize populates the cache from blockchain at startup
func (cache *ActiveSecretCache) Initialize(ctx context.Context, client blockchain.ClientInterface, guardianAddr string, currentHeight int64) error {
	cache.logger.Info("Initializing active secret cache",
		zap.String("guardian_address", guardianAddr),
		zap.Int64("current_height", currentHeight))

	// Fetch all secrets for guardian from blockchain
	secrets, err := client.ListSecretsForGuardian(ctx, guardianAddr)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets for guardian: %w", err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	// Filter and cache only actionable secrets
	actionableCount := 0
	for _, secret := range secrets {
		if cache.isActionableSecret(&secret, guardianAddr, currentHeight) {
			cache.addSecretUnsafe(&secret, guardianAddr, currentHeight)
			actionableCount++
		}
	}

	cache.logger.Info("Active secret cache initialized",
		zap.Int("total_secrets_fetched", len(secrets)),
		zap.Int("actionable_secrets_cached", actionableCount),
		zap.Int("awaiting_confirmation", len(cache.awaitingConfirmation)),
		zap.Int("pending_reveal", len(cache.pendingReveal)))

	return nil
}

// UpdateFromBlockchain updates cache with current blockchain state
func (cache *ActiveSecretCache) UpdateFromBlockchain(ctx context.Context, client blockchain.ClientInterface, guardianAddr string, currentHeight int64) error {
	// Fetch current secrets from blockchain
	secrets, err := client.ListSecretsForGuardian(ctx, guardianAddr)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets for guardian: %w", err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	// Track secrets we've seen in this update
	seenSecrets := make(map[string]bool, len(secrets))

	// Process all secrets from blockchain
	newSecrets := 0
	updatedSecrets := 0
	for _, secret := range secrets {
		seenSecrets[secret.ID] = true

		cached := cache.secrets[secret.ID]
		if cached == nil {
			// New secret - check if actionable
			if cache.isActionableSecret(&secret, guardianAddr, currentHeight) {
				cache.addSecretUnsafe(&secret, guardianAddr, currentHeight)
				newSecrets++
			}
		} else {
			// Update existing - idempotent overwrite
			cache.updateSecretUnsafe(&secret, guardianAddr, currentHeight)
			updatedSecrets++
		}
	}

	// Remove any cached secrets that are no longer returned by the blockchain
	// (should rarely happen, but ensures consistency)
	removedSecrets := 0
	for secretID := range cache.secrets {
		if !seenSecrets[secretID] {
			cache.evictSecretUnsafe(secretID)
			removedSecrets++
		}
	}

	cache.logger.Debug("Cache updated from blockchain",
		zap.Int("new_secrets", newSecrets),
		zap.Int("updated_secrets", updatedSecrets),
		zap.Int("removed_secrets", removedSecrets),
		zap.Int("total_cached", len(cache.secrets)))

	// Run cleanup if it's time
	cache.cleanupIfNeeded(currentHeight)

	return nil
}

// cleanupIfNeeded runs eviction cleanup if the cleanup interval has passed
func (cache *ActiveSecretCache) cleanupIfNeeded(currentHeight int64) {
	if currentHeight-cache.lastCleanupBlock < cache.cleanupInterval {
		return // Not time for cleanup yet
	}

	evictedCount := 0
	for secretID, cached := range cache.secrets {
		if cache.shouldEvict(cached, currentHeight) {
			cache.evictSecretUnsafe(secretID)
			evictedCount++
		}
	}

	cache.lastCleanupBlock = currentHeight

	if evictedCount > 0 {
		cache.logger.Info("Cache cleanup completed",
			zap.Int("evicted_secrets", evictedCount),
			zap.Int64("cleanup_block", currentHeight),
			zap.Int("remaining_secrets", len(cache.secrets)))
	}
}

// MarkRevealed marks a secret as revealed by this guardian
func (cache *ActiveSecretCache) MarkRevealed(secretID string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	cached := cache.secrets[secretID]
	if cached != nil && cached.LocalState != StateRevealed {
		cache.logger.Debug("Marking secret as revealed", zap.String("secret_id", secretID))

		// Remove from current state index
		cache.removeFromIndex(secretID)

		// Update state
		cached.LocalState = StateRevealed

		// Don't add to any index - revealed secrets don't need processing
	}
}

// EvictSecret removes a secret from cache (thread-safe)
func (cache *ActiveSecretCache) EvictSecret(secretID string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.evictSecretUnsafe(secretID)
}

// addSecretUnsafe adds a secret to cache without locking (internal use)
func (cache *ActiveSecretCache) addSecretUnsafe(secret *blockchain.Secret, guardianAddr string, currentHeight int64) {
	assignment := cache.findAssignment(secret, guardianAddr)
	if assignment == nil {
		return // Not assigned to us
	}

	localState := cache.determineLocalState(secret, assignment, currentHeight)

	cached := &CachedSecret{
		Secret:      secret,
		Assignment:  assignment,
		LocalState:  localState,
		LastUpdated: currentHeight,
		AddedAt:     time.Now(),
	}

	cache.secrets[secret.ID] = cached
	cache.addToIndex(secret.ID, cached)

	cache.logger.Debug("Added secret to cache",
		zap.String("secret_id", secret.ID),
		zap.String("state", secret.State),
		zap.String("local_state", localState.String()),
		zap.String("assignment_status", assignment.Status))
}

// updateSecretUnsafe updates an existing secret in cache without locking
func (cache *ActiveSecretCache) updateSecretUnsafe(secret *blockchain.Secret, guardianAddr string, currentHeight int64) {
	cached := cache.secrets[secret.ID]
	if cached == nil {
		// Secret not in cache, add if actionable
		if cache.isActionableSecret(secret, guardianAddr, currentHeight) {
			cache.addSecretUnsafe(secret, guardianAddr, currentHeight)
		}
		return
	}

	assignment := cache.findAssignment(secret, guardianAddr)
	if assignment == nil {
		// No longer assigned to us, evict
		cache.evictSecretUnsafe(secret.ID)
		return
	}

	// Determine new state, but preserve StateRevealed if already set
	oldState := cached.LocalState
	var newState SecretLocalState
	if oldState == StateRevealed {
		// Once revealed, stay revealed until evictable
		if secret.State == "revealed" || secret.State == "cancelled" || secret.State == "failed" {
			newState = StateEvictable
		} else {
			newState = StateRevealed
		}
	} else {
		newState = cache.determineLocalState(secret, assignment, currentHeight)
	}

	// Remove from old index if state changed
	if newState != oldState {
		cache.removeFromIndex(secret.ID)
	}

	// Update cached data
	cached.Secret = secret
	cached.Assignment = assignment
	cached.LocalState = newState
	cached.LastUpdated = currentHeight

	// Add to new index if needed
	if newState != oldState {
		cache.addToIndex(secret.ID, cached)

		cache.logger.Debug("Secret state changed",
			zap.String("secret_id", secret.ID),
			zap.String("old_state", oldState.String()),
			zap.String("new_state", newState.String()),
			zap.String("secret_state", secret.State),
			zap.String("assignment_status", assignment.Status))
	}

	// Check if should evict after update
	if cache.shouldEvict(cached, currentHeight) {
		cache.evictSecretUnsafe(secret.ID)
	}
}

// evictSecretUnsafe removes a secret from cache without locking
func (cache *ActiveSecretCache) evictSecretUnsafe(secretID string) {
	cached := cache.secrets[secretID]
	if cached == nil {
		return
	}

	cache.logger.Debug("Evicting secret from cache",
		zap.String("secret_id", secretID),
		zap.String("local_state", cached.LocalState.String()),
		zap.Duration("cache_age", time.Since(cached.AddedAt)))

	// Remove from all indices
	cache.removeFromIndex(secretID)

	// Remove from main cache
	delete(cache.secrets, secretID)
}
