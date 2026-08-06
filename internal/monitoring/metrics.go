package monitoring

import "time"

// Nil-safe recording helpers: the guardian service records through these so
// a service constructed without monitoring (tests, one-shot commands) needs
// no wiring — a nil *Metrics is a no-op sink.

// RecordReveal counts a reveal attempt and, when the reveal succeeded, how far
// past window-open the share was submitted — in BLOCKS, which is what the
// guardian knows exactly. Seconds would require a cadence it deliberately does
// not carry, and which differs between networks and test runs.
func (m *Metrics) RecordReveal(success bool, blocksSinceWindowOpen int64) {
	if m == nil {
		return
	}
	if success {
		m.SuccessfulReveals.Inc()
		if blocksSinceWindowOpen >= 0 {
			m.RevealTiming.Observe(float64(blocksSinceWindowOpen))
		}
	} else {
		m.FailedReveals.Inc()
		m.TransactionFailures.WithLabelValues("reveal").Inc()
	}
}

// RecordConfirmation counts an assignment acceptance or rejection.
func (m *Metrics) RecordConfirmation(accepted bool) {
	if m == nil {
		return
	}
	if accepted {
		m.AssignmentsAccepted.Inc()
	} else {
		m.AssignmentsRejected.Inc()
	}
}

// RecordConfirmationFailure counts a failed confirmation transaction.
func (m *Metrics) RecordConfirmationFailure() {
	if m == nil {
		return
	}
	m.TransactionFailures.WithLabelValues("confirm").Inc()
}

// RecordValidationFailure counts a share-validation failure by type.
func (m *Metrics) RecordValidationFailure(kind string) {
	if m == nil {
		return
	}
	m.ValidationFailures.WithLabelValues(kind).Inc()
}

// RecordProcessingCycle records one monitoring cycle's duration and state.
func (m *Metrics) RecordProcessingCycle(d time.Duration, activeSecrets int, height int64) {
	if m == nil {
		return
	}
	m.ProcessingLatency.Observe(d.Seconds())
	m.SecretsProcessed.Inc()
	m.ActiveSecrets.Set(float64(activeSecrets))
	if height > 0 {
		m.LastBlockHeight.Set(float64(height))
	}
}

// RecordError counts a generic processing error.
func (m *Metrics) RecordError() {
	if m == nil {
		return
	}
	m.ErrorCount.Inc()
}

// RecordWindowMissed counts a reveal window that passed without our reveal —
// the leading indicator of a slash (missed-while-down included).
func (m *Metrics) RecordWindowMissed() {
	if m == nil {
		return
	}
	m.WindowsMissed.Inc()
}

// SetBalance records the guardian's account balance.
func (m *Metrics) SetBalance(amount float64) {
	if m == nil {
		return
	}
	m.GuardianBalance.Set(amount)
}
