package guardian

import (
	"sync"
	"time"
)

// Since-process-start observation buffers for the operator dashboard
// (dashboard plan §4). Deliberately in-memory and capped: the plan rules out
// dashboard persistence, because long-horizon history belongs to the metrics
// work-stream (Prometheus/Grafana) and per-secret forensics stay
// chain-queryable for the terminal-retention window. A restart clears these,
// and every panel built on them says so.
//
// These record what the daemon ALREADY observes while doing its job — no new
// chain queries, no new subscriptions. Recording is best-effort and must never
// block or fail the work it describes.

// Buffer caps. Sized to cover a busy operator's recent history without letting
// a long-running daemon grow unboundedly: at the 100-bond concurrency cap,
// these hold several complete assignment lifecycles.
const (
	maxDecisions   = 256
	maxSubmissions = 256
	maxSettlements = 256
)

// DecisionOutcome is what the daemon did with a candidacy.
type DecisionOutcome string

const (
	DecisionAccepted DecisionOutcome = "accepted"
	DecisionRejected DecisionOutcome = "rejected"
)

// Decision records one accept/reject with the reason the daemon recorded, so
// an operator can answer "why did I not take that secret?" without grepping
// logs. Reason is free text from the decision site rather than an enum: the
// causes (float insufficient, concurrency cap, policy declined, HMAC failure)
// arise in different packages and a shared enum would couple them.
type Decision struct {
	At       time.Time       `json:"at"`
	SecretID string          `json:"secret_id"`
	Outcome  DecisionOutcome `json:"outcome"`
	Reason   string          `json:"reason,omitempty"`
	Height   int64           `json:"height,omitempty"`
}

// SubmissionKind distinguishes the two transactions a guardian sends.
type SubmissionKind string

const (
	SubmissionConfirm SubmissionKind = "confirm"
	SubmissionReveal  SubmissionKind = "reveal"
)

// Submission records a broadcast outcome. Err carries the failure text when
// Success is false — an operator's first question about a failed reveal is
// what the chain said.
type Submission struct {
	At       time.Time      `json:"at"`
	Kind     SubmissionKind `json:"kind"`
	SecretID string         `json:"secret_id"`
	TxHash   string         `json:"tx_hash,omitempty"`
	Success  bool           `json:"success"`
	Err      string         `json:"error,omitempty"`
	Height   int64          `json:"height,omitempty"`
}

// Settlement records a settlement the daemon observed touching its own
// secrets, including the stalled case the plan calls out (panel 4).
type Settlement struct {
	At       time.Time `json:"at"`
	SecretID string    `json:"secret_id"`
	Outcome  string    `json:"outcome"`
	Stalled  bool      `json:"stalled"`
	Height   int64     `json:"height,omitempty"`
}

// Observations holds the capped buffers. Safe for concurrent use: the daemon
// records from its monitoring goroutines while the dashboard reads from an
// HTTP handler.
type Observations struct {
	mu          sync.RWMutex
	decisions   []Decision
	submissions []Submission
	settlements []Settlement
	startedAt   time.Time
	// Counters survive eviction from the ring, so the dashboard can say "12 of
	// 300 shown" rather than silently presenting a truncated view as complete.
	totalDecisions   int
	totalSubmissions int
	totalSettlements int
}

// NewObservations returns empty buffers stamped with the process start, which
// is what makes "since start" a statement rather than an assumption.
func NewObservations(startedAt time.Time) *Observations {
	return &Observations{startedAt: startedAt}
}

// appendCapped keeps the newest n entries, dropping from the front.
func appendCapped[T any](buf []T, v T, cap int) []T {
	buf = append(buf, v)
	if len(buf) > cap {
		// Copy down rather than reslice: reslicing would retain the whole
		// backing array for the life of the process.
		copy(buf, buf[len(buf)-cap:])
		buf = buf[:cap]
	}
	return buf
}

// RecordDecision notes an accept/reject. Nil-safe so call sites need no guard.
func (o *Observations) RecordDecision(d Decision) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.decisions = appendCapped(o.decisions, d, maxDecisions)
	o.totalDecisions++
}

// RecordSubmission notes a broadcast outcome. Nil-safe.
func (o *Observations) RecordSubmission(s Submission) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.submissions = appendCapped(o.submissions, s, maxSubmissions)
	o.totalSubmissions++
}

// RecordSettlement notes an observed settlement. Nil-safe.
func (o *Observations) RecordSettlement(s Settlement) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.settlements = appendCapped(o.settlements, s, maxSettlements)
	o.totalSettlements++
}

// ObservationsSnapshot is a point-in-time copy for the dashboard. Newest
// first, because every panel built on it is a "recent activity" view.
type ObservationsSnapshot struct {
	StartedAt        time.Time    `json:"started_at"`
	Decisions        []Decision   `json:"decisions"`
	Submissions      []Submission `json:"submissions"`
	Settlements      []Settlement `json:"settlements"`
	TotalDecisions   int          `json:"total_decisions"`
	TotalSubmissions int          `json:"total_submissions"`
	TotalSettlements int          `json:"total_settlements"`
}

// Snapshot copies the buffers under the read lock, newest first. The copies
// are what let the handler serialise without holding the daemon's lock across
// JSON encoding.
func (o *Observations) Snapshot() ObservationsSnapshot {
	if o == nil {
		return ObservationsSnapshot{
			Decisions:   []Decision{},
			Submissions: []Submission{},
			Settlements: []Settlement{},
		}
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return ObservationsSnapshot{
		StartedAt:        o.startedAt,
		Decisions:        reversedCopy(o.decisions),
		Submissions:      reversedCopy(o.submissions),
		Settlements:      reversedCopy(o.settlements),
		TotalDecisions:   o.totalDecisions,
		TotalSubmissions: o.totalSubmissions,
		TotalSettlements: o.totalSettlements,
	}
}

// reversedCopy returns a newest-first copy. Never nil — a nil slice would
// serialise as JSON null and force every consumer to handle two empty forms.
func reversedCopy[T any](in []T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
