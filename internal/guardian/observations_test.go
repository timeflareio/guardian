package guardian

import (
	"sync"
	"testing"
	"time"
)

// The observation buffers are the dashboard's only history, so the properties
// under test are the ones a panel would misreport if they broke: newest-first
// ordering, eviction that keeps the NEWEST rather than the oldest, totals that
// survive eviction, and nil-safety at every call site.

func TestObservationsNewestFirst(t *testing.T) {
	obs := NewObservations(time.Now())
	for i := 0; i < 3; i++ {
		obs.RecordDecision(Decision{SecretID: string(rune('a' + i)), Outcome: DecisionAccepted})
	}
	snap := obs.Snapshot()
	if len(snap.Decisions) != 3 {
		t.Fatalf("want 3 decisions, got %d", len(snap.Decisions))
	}
	// Newest first: every panel built on these is a "recent activity" view, and
	// oldest-first would bury the entry an operator is looking for.
	if got := snap.Decisions[0].SecretID; got != "c" {
		t.Errorf("newest entry should be first, got %q", got)
	}
	if got := snap.Decisions[2].SecretID; got != "a" {
		t.Errorf("oldest entry should be last, got %q", got)
	}
}

func TestObservationsEvictsOldestKeepsNewest(t *testing.T) {
	obs := NewObservations(time.Now())
	total := maxDecisions + 50
	for i := 0; i < total; i++ {
		obs.RecordDecision(Decision{Height: int64(i), Outcome: DecisionAccepted})
	}
	snap := obs.Snapshot()
	if len(snap.Decisions) != maxDecisions {
		t.Fatalf("buffer should cap at %d, got %d", maxDecisions, len(snap.Decisions))
	}
	// The newest must survive. Dropping the newest instead of the oldest would
	// make the panel show ancient history and hide what just happened.
	if got := snap.Decisions[0].Height; got != int64(total-1) {
		t.Errorf("newest height should be %d, got %d", total-1, got)
	}
	if got := snap.Decisions[len(snap.Decisions)-1].Height; got != int64(total-maxDecisions) {
		t.Errorf("oldest retained height should be %d, got %d", total-maxDecisions, got)
	}
	// The total survives eviction, which is what lets a panel say "256 of 306
	// shown" instead of passing a truncated view off as complete.
	if snap.TotalDecisions != total {
		t.Errorf("total should count every decision ever recorded, want %d got %d", total, snap.TotalDecisions)
	}
}

func TestObservationsNilSafe(t *testing.T) {
	// Call sites must not need a guard: recording is best-effort and must never
	// affect the work it describes.
	var obs *Observations
	obs.RecordDecision(Decision{})
	obs.RecordSubmission(Submission{})
	obs.RecordSettlement(Settlement{})

	snap := obs.Snapshot()
	// Empty slices, never nil: nil would serialise as JSON null and force every
	// consumer to handle two different empty forms.
	if snap.Decisions == nil || snap.Submissions == nil || snap.Settlements == nil {
		t.Error("a nil Observations must still snapshot to empty slices, not nils")
	}
}

func TestObservationsEmptySnapshotHasEmptySlices(t *testing.T) {
	snap := NewObservations(time.Now()).Snapshot()
	if snap.Decisions == nil || snap.Submissions == nil || snap.Settlements == nil {
		t.Error("empty buffers must snapshot to empty slices, not nils")
	}
	if len(snap.Decisions) != 0 {
		t.Errorf("expected no decisions, got %d", len(snap.Decisions))
	}
}

func TestObservationsConcurrentRecordAndSnapshot(t *testing.T) {
	// The daemon records from its monitoring goroutines while an HTTP handler
	// snapshots. Run under -race, this is the test that would catch a missing
	// lock.
	obs := NewObservations(time.Now())
	stop := make(chan struct{})

	// Writers and the reader are waited on SEPARATELY: the reader only exits
	// once stop is closed, and stop is only closed once the writers are done,
	// so a single WaitGroup covering both deadlocks.
	var writers, reader sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func(n int) {
			defer writers.Done()
			for j := 0; j < 200; j++ {
				obs.RecordSubmission(Submission{Kind: SubmissionReveal, Height: int64(j)})
				obs.RecordDecision(Decision{Outcome: DecisionRejected})
				obs.RecordSettlement(Settlement{Stalled: n%2 == 0})
			}
		}(i)
	}
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = obs.Snapshot()
			}
		}
	}()

	writers.Wait()
	close(stop)
	reader.Wait()

	snap := obs.Snapshot()
	if snap.TotalSubmissions != 800 {
		t.Errorf("want 800 submissions recorded, got %d", snap.TotalSubmissions)
	}
	if len(snap.Submissions) != maxSubmissions {
		t.Errorf("want the buffer capped at %d, got %d", maxSubmissions, len(snap.Submissions))
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	// A snapshot must not alias the live buffer, or a handler serialising it
	// would see entries mutate mid-encode.
	obs := NewObservations(time.Now())
	obs.RecordDecision(Decision{SecretID: "original"})
	snap := obs.Snapshot()
	snap.Decisions[0].SecretID = "mutated"

	if again := obs.Snapshot(); again.Decisions[0].SecretID != "original" {
		t.Error("mutating a snapshot must not reach the live buffer")
	}
}
