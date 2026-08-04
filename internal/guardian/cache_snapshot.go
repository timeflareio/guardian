package guardian

import (
	"time"

	"github.com/timeflareio/guardian/internal/chain"
)

// Read snapshots of the active-secret cache for the operator dashboard
// (dashboard plan §4). Snapshots exist because the cache's existing accessors
// hand out *CachedSecret — pointers into live entries the daemon mutates as
// state advances. A dashboard handler ranging over those while a monitoring
// tick updates them is a data race, and the values would be inconsistent even
// when it got away with it. These copy what a panel needs under the read lock
// and hand back plain values.

// AssignmentSnapshot is one cached assignment, flattened for display.
type AssignmentSnapshot struct {
	SecretID string `json:"secret_id"`
	// ChainState is the secret's on-chain FSM state; LocalState is the
	// daemon's own derived processing state. Both are shown, because "the
	// chain says pending, I say needs-reveal" is exactly the pairing an
	// operator needs when a reveal has not gone out.
	ChainState string `json:"chain_state"`
	LocalState string `json:"local_state"`
	// AssignmentStatus is our own assignment's status (proposed, accepted…).
	AssignmentStatus string `json:"assignment_status"`

	Threshold        int64 `json:"threshold"`
	MinShares        int64 `json:"min_shares"`
	MaxShares        int64 `json:"max_shares"`
	AcceptedCount    int64 `json:"accepted_count"`
	CommitDeadline   int64 `json:"commit_deadline"`
	RevealStartBlock int64 `json:"reveal_start_block"`
	RevealEndBlock   int64 `json:"reveal_end_block"`
	CreatedAt        int64 `json:"created_at"`

	// BondUveil is our own frozen bond; RewardPoolUveil the whole pool. The
	// per-guardian floor is pool ÷ max_shares — a floor, not a promise,
	// because a smaller accepted roster divides the same pool fewer ways.
	BondUveil       int64  `json:"bond_uveil"`
	RewardPoolUveil string `json:"reward_pool_uveil"`

	// Revealed is whether OUR share is already on chain.
	Revealed bool `json:"revealed"`
	// KeyEpochBound is the epoch our assignment's share was encrypted to,
	// derived from the creation height. Assignments are permanently bound to
	// it, so this is what makes rotation wind-down answerable locally.
	LastUpdated int64     `json:"last_updated_height"`
	AddedAt     time.Time `json:"added_at"`
}

// CacheSnapshot is the whole cache at one instant.
type CacheSnapshot struct {
	Assignments []AssignmentSnapshot `json:"assignments"`
	// StateCounts is keyed by local-state name.
	StateCounts map[string]int `json:"state_counts"`
	Size        int            `json:"size"`
}

// Snapshot copies every cached assignment under the read lock. Address is the
// daemon's own guardian address, needed to pick our assignment and our bond
// out of the secret's rosters.
func (cache *ActiveSecretCache) Snapshot(address string) CacheSnapshot {
	cache.mu.RLock()
	defer cache.mu.RUnlock()

	out := CacheSnapshot{
		Assignments: make([]AssignmentSnapshot, 0, len(cache.secrets)),
		StateCounts: make(map[string]int, len(cache.secrets)),
		Size:        len(cache.secrets),
	}
	for id, cached := range cache.secrets {
		if cached == nil || cached.Secret == nil {
			continue
		}
		s := cached.Secret
		snap := AssignmentSnapshot{
			SecretID:         id,
			ChainState:       s.State,
			LocalState:       cached.LocalState.String(),
			Threshold:        s.Threshold,
			MinShares:        s.MinShares,
			MaxShares:        s.MaxShares,
			AcceptedCount:    s.AcceptedCount,
			CommitDeadline:   s.CommitDeadline,
			RevealStartBlock: s.RevealStartBlock,
			RevealEndBlock:   s.RevealEndBlock,
			CreatedAt:        s.CreatedAt,
			RewardPoolUveil:  s.RewardPool.Amount,
			LastUpdated:      cached.LastUpdated,
			AddedAt:          cached.AddedAt,
		}
		if cached.Assignment != nil {
			snap.AssignmentStatus = cached.Assignment.Status
		}
		if bond, ok := s.BondFor(address); ok {
			snap.BondUveil = bond
		}
		snap.Revealed = revealedByUs(s, address)
		out.Assignments = append(out.Assignments, snap)
		out.StateCounts[cached.LocalState.String()]++
	}
	return out
}

// revealedByUs reports whether our own share is already on chain. Checked by
// address rather than by count: a secret with revealed shares is not evidence
// that OURS is one of them, and that distinction is the difference between a
// calm dashboard and a missed window.
func revealedByUs(s *chain.Secret, address string) bool {
	for _, r := range s.RevealedShares {
		if r.GuardianAddress == address {
			return true
		}
	}
	return false
}
