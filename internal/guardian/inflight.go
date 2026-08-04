package guardian

import "sync"

// In-flight submission tracking, closing the gap between "broadcast" and
// "reconciled against chain state".
//
// A broadcast returns once the transaction passes CheckTx, not once it is in a
// block. Until it is included, chain state still shows the work as outstanding,
// so a poll cycle that completes inside one block time sees the same assignment
// as unhandled and submits again. The chain rejects the duplicate, but the
// guardian has already paid its fee — a systematic, unreimbursed cost on every
// assignment.
//
// This registry is deliberately NOT built on Observations. That buffer records
// the same broadcasts and looks like the natural home, but it is a capped,
// lossy ring whose contract is that recording must never affect the work it
// describes. A guard that decides whether to spend a fee cannot sit on a
// structure allowed to forget.
//
// It is in-memory and per-process, cleared on restart: a restart mid-flight
// costs one duplicate, which self-corrects on the following cycle, and
// persisting it would buy a fraction of one fee at the price of a durable store
// the daemon does not otherwise need.

// inFlightExpiryBlocks is how long a recorded submission is treated as
// outstanding before the work becomes eligible again.
//
// Inclusion normally takes one block, so five absorbs ordinary congestion while
// leaving fifteen of the twenty-block MinCommitTimeout for a genuine retry if
// the transaction was dropped from the mempool rather than included. It must
// also stay under the reveal path's evict-and-re-add recovery, or the guard
// would turn a case that self-corrects today into a missed reveal window —
// which costs 50% of the bond, far more than the duplicate fee this saves.
const inFlightExpiryBlocks = int64(5)

// inFlightKey identifies one outstanding submission.
//
// The kind is SubmissionKind rather than a parallel enum of its own: the
// registry gates exactly the transactions Observations already records, and two
// vocabularies for one set of transactions would drift. Accept and reject share
// the "confirm" kind because they are the same chain message with a different
// bool — sending one must suppress the other, which a shared key gives for free.
type inFlightKey struct {
	secretID string
	kind     SubmissionKind
}

// InFlightRegistry records submissions that have been broadcast but not yet
// observed on chain. Safe for concurrent use: reveals are submitted by parallel
// workers and can be triggered by the poll loop and a block-header event at the
// same time.
type InFlightRegistry struct {
	mu sync.Mutex
	// entries maps a submission to the height it was broadcast at.
	entries map[inFlightKey]int64
}

// NewInFlightRegistry returns an empty registry.
func NewInFlightRegistry() *InFlightRegistry {
	return &InFlightRegistry{entries: make(map[inFlightKey]int64)}
}

// Reserve claims the right to submit, reporting false when a submission of the
// same kind for the same secret is already outstanding.
//
// The check and the record are one locked operation on purpose. Consulting the
// registry and then submitting would leave a window in which two callers both
// read "clear" — reintroducing the duplicate this exists to prevent, and doing
// so precisely under the concurrency the reveal path already has.
func (r *InFlightRegistry) Reserve(secretID string, kind SubmissionKind, height int64) bool {
	if r == nil {
		return true
	}
	key := inFlightKey{secretID: secretID, kind: kind}

	r.mu.Lock()
	defer r.mu.Unlock()

	if at, ok := r.entries[key]; ok && height < at+inFlightExpiryBlocks {
		return false
	}
	r.entries[key] = height
	return true
}

// Release drops a reservation whose broadcast did not happen, so the work is
// retried on the next cycle rather than waiting out the expiry.
//
// This covers a failure at CheckTx, which is reported synchronously. A
// transaction that passes CheckTx and then fails in DeliverTx cannot be
// detected here — that is what the expiry is for.
func (r *InFlightRegistry) Release(secretID string, kind SubmissionKind) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, inFlightKey{secretID: secretID, kind: kind})
}

// Retain drops every entry for which keep reports false, and is how the
// registry reconciles: an entry whose work the chain has now recorded is no
// longer outstanding and should not hold its slot until expiry.
func (r *InFlightRegistry) Retain(keep func(secretID string, kind SubmissionKind) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.entries {
		if !keep(key.secretID, key.kind) {
			delete(r.entries, key)
		}
	}
}

// Len reports how many submissions are currently outstanding.
func (r *InFlightRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
