package guardian

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/timeflareio/guardian/blockchain"
	"github.com/timeflareio/guardian/config"
)

// EpochKeyResolver resolves which key epoch an assignment was encrypted to
// and serves that epoch's private key — fully automated, the operator is
// never asked which key applies (docs/spec.md "Guardian Key Rotation").
//
// The chain's rule: the epoch in force for a guardian at height h is the
// newest key-history entry with effective_from_height <= h. Creators encrypt
// to the key handed over at selection, so the epoch of an assignment derives
// from its secret's creation (= selection) height. The current epoch's key
// lives at the configured private-key path; each retired epoch N sits beside
// it as <path>.epoch<N> until its last assignment settles.
type EpochKeyResolver struct {
	cfg             *config.Config
	client          blockchain.ClientInterface
	logger          *zap.Logger
	guardianAddress string

	mu      sync.RWMutex
	history []blockchain.KeyEpoch // epoch order; nil until first fetch
}

// NewEpochKeyResolver creates a resolver for the configured guardian.
func NewEpochKeyResolver(cfg *config.Config, client blockchain.ClientInterface, logger *zap.Logger) *EpochKeyResolver {
	return &EpochKeyResolver{
		cfg:             cfg,
		client:          client,
		logger:          logger,
		guardianAddress: cfg.GuardianAddress,
	}
}

// Refresh fetches the guardian's key history from the chain. When the current
// epoch has advanced past what was cached (a rotation ran — possibly from a
// separate `guardiand rotate-key` process, which rewrites the key files on
// disk), the in-memory key cache is wiped so the next use reloads from disk.
func (r *EpochKeyResolver) Refresh(ctx context.Context) error {
	history, err := r.client.GetGuardianKeyHistory(ctx, r.guardianAddress)
	if err != nil {
		return fmt.Errorf("failed to fetch key history: %w", err)
	}

	r.mu.Lock()
	previousMax := maxEpoch(r.history)
	r.history = history
	r.mu.Unlock()

	if newMax := maxEpoch(history); newMax > previousMax && previousMax >= 0 {
		r.logger.Warn("Key epoch advanced on-chain — reloading local keys from disk",
			zap.Int64("previous_epoch", previousMax),
			zap.Int64("current_epoch", newMax))
		r.cfg.WipeEncryptionKey()
	}
	return nil
}

// maxEpoch returns the newest epoch in the history, or -1 for none.
func maxEpoch(history []blockchain.KeyEpoch) int64 {
	max := int64(-1)
	for _, e := range history {
		if int64(e.Epoch) > max { //nolint:gosec // epochs are tiny by construction
			max = int64(e.Epoch)
		}
	}
	return max
}

// snapshot returns the cached history, fetching it on first use.
func (r *EpochKeyResolver) snapshot(ctx context.Context) ([]blockchain.KeyEpoch, error) {
	r.mu.RLock()
	history := r.history
	r.mu.RUnlock()
	if history != nil {
		return history, nil
	}
	if err := r.Refresh(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.history, nil
}

// EpochAt derives the epoch in force at the given height: the newest history
// entry with effective_from_height <= height.
func (r *EpochKeyResolver) EpochAt(ctx context.Context, height int64) (uint64, error) {
	history, err := r.snapshot(ctx)
	if err != nil {
		return 0, err
	}
	if len(history) == 0 {
		// No history on chain (pre-rotation state) — single-epoch guardian.
		return 0, nil
	}
	found := false
	epoch := uint64(0)
	for _, e := range history { // epoch order; keep the newest that qualifies
		if e.EffectiveFromHeight <= height {
			epoch = e.Epoch
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("no key epoch in force at height %d (epoch 0 effective from %d)",
			height, history[0].EffectiveFromHeight)
	}
	return epoch, nil
}

// KeyForHeight returns the private key of the epoch in force at the given
// height (the key an assignment selected at that height was encrypted to),
// plus the epoch it resolved.
func (r *EpochKeyResolver) KeyForHeight(ctx context.Context, height int64) ([32]byte, uint64, error) {
	epoch, err := r.EpochAt(ctx, height)
	if err != nil {
		return [32]byte{}, 0, err
	}

	history, _ := r.snapshot(ctx)
	current := uint64(0)
	if m := maxEpoch(history); m > 0 {
		current = uint64(m)
	}

	if epoch == current {
		key, err := r.cfg.GetEncryptionPrivateKey()
		return key, epoch, err
	}
	key, err := r.cfg.GetRetiredEpochKey(epoch)
	return key, epoch, err
}

// EpochKey pairs an epoch with its loaded private key, for the
// belt-and-braces trial-decrypt fallback.
type EpochKey struct {
	Epoch uint64
	Key   [32]byte
}

// TrialKeys returns every locally loadable epoch key, newest epoch first —
// the fallback path when derivation-based resolution fails to decrypt. Load
// failures are skipped (a retired key legitimately disappears once its last
// assignment settles).
func (r *EpochKeyResolver) TrialKeys(ctx context.Context) []EpochKey {
	var keys []EpochKey

	history, err := r.snapshot(ctx)
	if err != nil || len(history) == 0 {
		if key, kerr := r.cfg.GetEncryptionPrivateKey(); kerr == nil {
			keys = append(keys, EpochKey{Epoch: 0, Key: key})
		}
		return keys
	}

	current := uint64(0)
	if m := maxEpoch(history); m > 0 {
		current = uint64(m)
	}
	for i := len(history) - 1; i >= 0; i-- {
		epoch := history[i].Epoch
		var key [32]byte
		var kerr error
		if epoch == current {
			key, kerr = r.cfg.GetEncryptionPrivateKey()
		} else {
			key, kerr = r.cfg.GetRetiredEpochKey(epoch)
		}
		if kerr != nil {
			continue
		}
		keys = append(keys, EpochKey{Epoch: epoch, Key: key})
	}
	return keys
}

// MissingEpochKeys returns the epochs among `needed` whose keys are not
// locally available — the startup self-check refuses to run while any
// in-flight assignment's epoch key is missing.
func (r *EpochKeyResolver) MissingEpochKeys(ctx context.Context, needed map[uint64]bool) []uint64 {
	var missing []uint64
	for epoch := range needed {
		if _, _, err := r.keyForEpoch(ctx, epoch); err != nil {
			missing = append(missing, epoch)
		}
	}
	return missing
}

// keyForEpoch loads a specific epoch's key (current or retired).
func (r *EpochKeyResolver) keyForEpoch(ctx context.Context, epoch uint64) ([32]byte, uint64, error) {
	history, err := r.snapshot(ctx)
	if err != nil {
		return [32]byte{}, 0, err
	}
	current := uint64(0)
	if m := maxEpoch(history); m > 0 {
		current = uint64(m)
	}
	if epoch == current {
		key, kerr := r.cfg.GetEncryptionPrivateKey()
		return key, epoch, kerr
	}
	key, kerr := r.cfg.GetRetiredEpochKey(epoch)
	return key, epoch, kerr
}
