package monitoring

import (
	"sync/atomic"
	"time"
)

// Health is the shared liveness state between the guardian service (which
// records) and the monitoring endpoints (which report). All fields are
// atomics — no locks on hot paths.
type Health struct {
	lastPollUnixNano  atomic.Int64 // last successful poll/resync completion
	lastEventUnixNano atomic.Int64 // last event or block header received
	lastHeight        atomic.Int64
	chainReachable    atomic.Bool
	keyLoadable       atomic.Bool
	registered        atomic.Bool

	// maxStale is how old the freshest activity signal may be before the
	// guardian reports unready.
	maxStale time.Duration
}

// NewHealth creates a health tracker. maxStale bounds how old the freshest
// poll/event signal may be before readiness fails.
func NewHealth(maxStale time.Duration) *Health {
	if maxStale <= 0 {
		maxStale = 30 * time.Second
	}
	return &Health{maxStale: maxStale}
}

// RecordPoll marks a successful poll/resync cycle.
func (h *Health) RecordPoll() {
	h.lastPollUnixNano.Store(time.Now().UnixNano())
	h.chainReachable.Store(true)
}

// RecordEvent marks a received chain event or block header.
func (h *Health) RecordEvent() {
	h.lastEventUnixNano.Store(time.Now().UnixNano())
	h.chainReachable.Store(true)
}

// RecordHeight stores the latest observed block height.
func (h *Health) RecordHeight(height int64) {
	h.lastHeight.Store(height)
}

// SetChainReachable records chain connectivity state.
func (h *Health) SetChainReachable(ok bool) { h.chainReachable.Store(ok) }

// SetKeyLoadable records whether the share-decryption key loads.
func (h *Health) SetKeyLoadable(ok bool) { h.keyLoadable.Store(ok) }

// SetRegistered records on-chain registration state.
func (h *Health) SetRegistered(ok bool) { h.registered.Store(ok) }

// LastHeight returns the latest observed block height.
func (h *Health) LastHeight() int64 { return h.lastHeight.Load() }

// LastActivityAge returns the age of the freshest poll/event signal.
func (h *Health) LastActivityAge() time.Duration {
	newest := max(h.lastPollUnixNano.Load(), h.lastEventUnixNano.Load())
	if newest == 0 {
		return time.Duration(1<<63 - 1) // never
	}
	return time.Since(time.Unix(0, newest))
}

// Snapshot is the health state served by /health and /ready.
type Snapshot struct {
	Healthy         bool   `json:"-"`
	Ready           bool   `json:"-"`
	ChainReachable  bool   `json:"chain_reachable"`
	KeyLoadable     bool   `json:"key_loadable"`
	Registered      bool   `json:"registered"`
	LastActivityAge string `json:"last_activity_age"`
	LastHeight      int64  `json:"last_height"`
}

// Snapshot evaluates current health. Healthy = the process's dependencies
// work (chain reachable, key loads). Ready = healthy AND registered AND the
// monitoring loop has been active recently (a wedged loop reports unready so
// supervisors restart it).
func (h *Health) Snapshot() Snapshot {
	age := h.LastActivityAge()
	s := Snapshot{
		ChainReachable: h.chainReachable.Load(),
		KeyLoadable:    h.keyLoadable.Load(),
		Registered:     h.registered.Load(),
		LastHeight:     h.lastHeight.Load(),
	}
	if age > 100*365*24*time.Hour {
		s.LastActivityAge = "never"
	} else {
		s.LastActivityAge = age.Truncate(time.Millisecond).String()
	}
	s.Healthy = s.ChainReachable && s.KeyLoadable
	s.Ready = s.Healthy && s.Registered && age <= h.maxStale
	return s
}
