# Guardian accept and reveal decision correctness

| | |
|---|---|
| **Priority** | P0 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian |
| **Depends on** | [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md) (its phase 2, for this plan's phase 3) |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md); [PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md](PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md) |
| **Open questions** | 5 |

**Why this priority**: the daemon can permanently forfeit an assignment over a
transient local condition, can believe it revealed when it did not, and can be
blocked from retrying a dropped reveal until after its window has closed.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
findings 2, 12, 13, 14, 16, 18, 33.
**Already landed**: finding 17 (the `enable_hmac_validation` opt-out) and the
reveal jitter (`reveal_offset_blocks`) were both removed by commit f1a9265
(4 August 2026): HMAC validation is unconditional, and a share is revealed as
soon as its window opens. Neither is in scope here any longer.
**Components**: `internal/guardian/reveal.go`, `service.go`, `cache.go`,
`inflight.go`, `observations.go`; `internal/config/config.go`;
`internal/guardian/reveal_test.go`, `cache_test.go`, `service_test.go`,
`inflight_test.go`, `integration_test.go`.

**Protocol surface**: none. Every behaviour introduced here is already
sanctioned by the spec (`timeflareio/chain` — `docs/spec.md`) — Phase 3 names
non-response as a legitimate outcome and assigns capacity assessment to the
guardian; Settlement defines what counts as a correct reveal. No spec change is
required, and the plan must not be executed in a way that needs one.

---

## The issue

This is the cluster that decides *what the daemon submits and when*. Eight
defects, several interacting.

### 1. A transient local condition causes a permanent on-chain rejection

`processConfirmation` (`internal/guardian/reveal.go:107-120`): any decrypt
failure falls through to `rejectAssignment`, including
`ErrPrivateKeyUnavailable` — returned by `decryptShare` (`reveal.go:329-331`)
when no epoch key can be loaded at all.

The daemon contradicts itself here. Startup deliberately tolerates an
unloadable key so that "an operator can attach the passphrase file without a
crash loop" (`internal/guardian/service.go:158-175`, `:269-276`). The
confirmation path consults no such state, so during exactly that grace period
every arriving assignment is **rejected on chain**.

Rejection is final: the chain returns `ErrAlreadyResponded` for any status
other than `PROPOSED` (`timeflareio/chain` —
`x/secrets/keeper/msg_server_confirm_shares.go:67`), and spec.md Phase 3 states
"Decision finality means acceptance responses cannot be changed once
submitted."

A container restart whose passphrase secret mounts a minute late, with a
Phase-2 distribution landing in that minute, permanently forfeits the
assignment and pays an unreimbursed reject fee.

### 2. Rejection is the wrong default even when the share is genuinely bad

Every validation failure broadcasts `MsgGuardianConfirmShares{accept:false}`
(`rejectAssignment`, `reveal.go:372-399`). But non-response is a first-class
protocol outcome — spec.md Phase 3: "Candidates may reject, fail the float
check, or simply not respond — progression only requires the band's floor" —
and the accept-fee slices pay only acceptors and revealers, so reject gas is
never reimbursed. CHAIN_MECHANICS.md Security Observation §2
(`timeflareio/chain` — `docs/CHAIN_MECHANICS.md`) confirms non-response is
unpriced.

A creator distributing garbage to its draw therefore griefs every selected
guardian into a fee per secret.

### 3. Broadcast success is treated as reveal success

`processReveals` (`service.go:557-559`) calls `MarkRevealed` when
`ProcessReveal` returns nil, and `processReveal` (`reveal.go:207`) records
`metrics.RecordReveal(true, …)`. Both fire on `BROADCAST_MODE_SYNC` acceptance
(`internal/chain/signer.go`, `submitLocked`), which is CheckTx only.

Two consequences. A transaction that passes CheckTx and then fails in
DeliverTx — or is dropped from the mempool — leaves the daemon believing it
revealed, and the operator's telemetry reporting success, while a no-reveal
slash follows at settlement. And the reveal timing histogram measures
broadcasts, not reveals.

### 4. `ProcessReveal`'s nil return is overloaded

`ProcessReveal` (`reveal.go:141-164`) returns nil when a submission is already
in flight (`:152-157`), and `processReveals` (`service.go:546`) cannot
distinguish that from a successful broadcast.

- A secret whose reveal is merely in flight is marked `StateRevealed`, evicted
  on the next refresh and re-added — cache churn on the path where a retry may
  be needed.
- A falsely-marked secret whose window then closes is no longer
  `StateNeedsReveal`, so `checkMissedWindows` — which filters on that state
  (`service.go:588`) — can fail to count the miss. The slash indicator
  under-reports in precisely the case it exists for.
- Correctness now rests on `shouldEvict` evicting `StateRevealed`
  (`internal/guardian/cache.go:218-221`). `internal/guardian/inflight.go`
  already names this coupling as load-bearing. A future cache change that
  breaks it becomes a permanent missed reveal.

The timing gate at the top of `ProcessReveal` also returns nil, but it has no
live producer: `GetSecretsNeedingReveal` (`cache.go:121-132`) hands
`processReveals` only secrets already inside their window at the same height.

### 5. A dropped reveal near the window tail cannot be retried

After a broadcast passes CheckTx, `InFlightRegistry.Reserve` refuses
resubmission until `broadcastHeight + 5` (`inFlightExpiryBlocks`,
`inflight.go:35`). If the transaction is then dropped — node restart, mempool
eviction — the recovery path (preserve `StateRevealed` → evict → re-add as
`StateNeedsReveal`) runs, but the reservation blocks the retry.

The comment on that constant anticipates exactly this: the expiry "must also
stay under the reveal path's evict-and-re-add recovery, or the guard would turn
a case that self-corrects today into a missed reveal window — which costs 50%
of the bond". The reveal window is derived on chain and its floor is 50 blocks
(`RevealWindowFloor`, `timeflareio/chain` — `x/secrets/types/constants.go`), so
the exposed tail is 10% of the smallest window.

The same comment's arithmetic about the commit window is wrong and is corrected
by [PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md](PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md);
that plan changes the stated justification, not the value this section is
about.

### 6. Window and deadline edges are not respected on the confirm side

`processConfirmation` never reads `CommitDeadline` at all — the field is used
only by the dashboard (`internal/guardian/dashboard_source.go:168-177`) — while
the chain rejects past-deadline confirms (`timeflareio/chain` —
`x/secrets/keeper/msg_server_confirm_shares.go:43-47`). The commit window is a
fixed 50 blocks from creation (`CommitTimeoutBlocks`, same constants file). The
daemon therefore keeps retrying a futile confirmation, paying a fee each time,
until the cache evicts the secret.

The reveal side is subtler and is analysed under Design below.

### 7. No economic self-assessment, and the decision log says otherwise

`processConfirmation` (`reveal.go:86-138`) accepts on two conditions: the share
decrypts and the HMAC verifies. It never consults unlocked float, the frozen
bond, distance or the active bond count — yet `acceptAssignment` records
`Reason: "share HMAC verified, bond affordable"` (`reveal.go:356-359`). The log
asserts a check that does not exist.

The data is already fetched: `Secret.OurBondUveil`/`BondFor` and
`Guardian.Stake`/`LockedStake`/`BondK`/`ActiveBondCount`
(`internal/chain/types.go`) are consumed only by the dashboard.

The chain is the hard gate, so the daemon cannot over-commit: spec.md Phase 3
fails the accept when unlocked float is below the frozen bond, or at the
concurrency cap of 100 (`MaxActiveBondsPerGuardian`). But when short, the
daemon retries the doomed accept every poll tick until the commit deadline,
with no classification and no backoff. spec.md Phase 3 explicitly assigns this
responsibility to the client: "Capacity assessment is otherwise the guardian's
own responsibility — its client should decline assignments beyond what its
infrastructure can serve."

### 8. Data race on shared `*CachedSecret`

The cache reassigns `cached.Secret`, `cached.Assignment`, `cached.LocalState`
and `cached.LastUpdated` under `cache.mu` (`internal/guardian/cache.go`), while
`reconcileInFlight` (`service.go:485-501`), `checkMissedWindows` (`:586-597`)
and the reveal workers (`:539-560`) read those fields through shared pointers
with no lock held — `GetAll` (`cache.go:97-106`) copies the map, not the
entries. The polling loop and the event-monitor callbacks genuinely run
concurrently with no mutual exclusion.

Damage is bounded today — `EncryptedShare` is immutable per assignment and the
in-flight registry gates duplicates — but a reader can observe a torn pair, and
the existing `-race` suite does not cover it because the tests never run the
poll loop and the event monitor together.
`internal/guardian/cache_snapshot.go:9-15` documents this precise hazard and
fixes it for the dashboard while the daemon's own consumers still read live
pointers.

---

## Design

### Phase 1 — classify the confirmation decision

Replace the single "decrypt failed → reject" path with three outcomes:

| Condition | Action | Rationale |
|---|---|---|
| Key unavailable (`ErrPrivateKeyUnavailable`) | **Skip silently**, retry next cycle; record a decision with reason, raise a health signal | Local and transient; rejection would be permanent |
| Decrypt fails with a loadable key, or HMAC mismatches | **Skip by default**, record the reason | Positive evidence of a bad share, but non-response costs nothing and rejection costs a fee (issue 2) |
| Insufficient unlocked float, or at the local concurrency cap | **Skip**, record the reason, back off | The accept would fail in DeliverTx anyway |
| All checks pass | **Accept** | — |

The decision reason recorded by `acceptAssignment` becomes accurate: it names
the checks actually performed.

Whether the bad-share case should reject rather than skip is open question 1.

### Phase 2 — un-overload the reveal return

`ProcessReveal` returns an explicit outcome, one value per case that has a
producer:

```go
type RevealOutcome int

const (
    RevealSkippedInFlight RevealOutcome = iota
    RevealBroadcast
)
```

`processReveals` calls `MarkRevealed` only on `RevealBroadcast`, which removes
the churn cycle, restores `checkMissedWindows`'s ability to see a missed
window, and severs the accidental dependency on `shouldEvict`'s treatment of
`StateRevealed`.

### Phase 3 — reveal success means inclusion, not acceptance

`MarkRevealed` becomes provisional. A broadcast records the transaction hash
and the height, and the state is confirmed only when a subsequent cache refresh
observes our reveal on chain (`Secret.HasRevealed`, already used by
`reconcileInFlight` at `service.go:497`). Until then the secret stays eligible
for retry.

`metrics.RecordReveal(true, …)` moves to the confirmation point, so the timing
histogram measures reveals rather than broadcasts. A separate counter records
broadcasts, because the gap between the two is exactly what an operator
debugging a slash needs to see.

This phase depends on
[PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
phase 2: inclusion-based success only means something if a submission is not
silently abandoned at shutdown.

### Phase 4 — window and deadline edges

**Confirm side** is unambiguous: stop attempting once the observed height
exceeds `CommitDeadline`. The transaction cannot succeed and the fee is wasted.

**Reveal side** needs care, and the sweep's initial "last safe broadcast is
`end − 1`" is too blunt. A transaction broadcast when our *observed* committed
height is `end` reaches a block at `end + 1` and is rejected by the chain
(`timeflareio/chain` — `x/secrets/keeper/reveal_window.go:30`). But our
observed height can lag the network, in which case the same broadcast lands at
`end` and succeeds. The asymmetry decides it: attempting costs one fee, not
attempting costs 40% of the bond burned plus 10% to the creator.

So the design keeps the attempt at the edge and fixes the accounting around it:

- Continue broadcasting while `observedHeight <= RevealEndBlock`.
- Never treat such a broadcast as success (phase 3 already ensures this).
- Log and count it distinctly as a last-chance attempt, so an operator sees
  that the daemon was at the edge rather than comfortably inside the window.
- Nothing in the *scheduling* reaches the edge: `shouldRevealNow`
  (`reveal.go:258-274`) reveals as soon as the window opens. The real exposure
  is discovering a secret late, which
  [PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md](PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md)
  addresses.

### Phase 5 — in-flight expiry must not outlive the window

`Reserve` gains awareness of the deadline it is gating. When the remaining
window is shorter than `inFlightExpiryBlocks`, the reservation expires at the
window edge instead:

```go
func (r *InFlightRegistry) Reserve(secretID string, kind SubmissionKind, height, deadline int64) bool
```

with the effective expiry `min(height + inFlightExpiryBlocks, deadline)`. The
chain rejects true duplicates for one fee, which is cheap insurance against
losing half a bond.

### Phase 6 — float and capacity policy

Before accepting, compare the secret's frozen bond for this guardian
(`Secret.BondFor`) against unlocked float (`Stake − LockedStake`), and the
active bond count against the local cap. Both values are already on hand.

This is also where `max_concurrent_secrets` — currently dead (sweep finding 39)
— either becomes real or is deleted; see open question 3.

Repeated shortfall should back off rather than retry every tick, and record a
decision the dashboard and logs can show, so "why did I not take that secret?"
is answerable.

### Phase 7 — close the cache race

Accessors return values rather than shared pointers. `CachedSecret`'s fields
are only ever *reassigned* (the `Secret` and `Assignment` objects themselves
are never mutated after construction), so copying the pointer pair plus
`LocalState` and `LastUpdated` into a value struct under `RLock` is sufficient
and cheap. `cache_snapshot.go` already establishes the pattern.

A regression test must run the poll loop and the event monitor concurrently
under `-race`; the current suite's failure to do so is why this went unnoticed.

---

## What this plan does not solve

- **It does not change any protocol rule.** No proto, no keeper, no spec.md
  change. If execution finds itself wanting one, that is a signal to stop and
  raise it, not to proceed.
- **It does not stop a guardian being slashed for genuine unavailability.** If
  the key is missing at reveal time rather than at confirm time, the bond is
  already locked and the slash follows — that is CHAIN_MECHANICS.md Trade-off §14.
- **It does not make discovery timely.** A secret found late still reveals
  late; the full-store rescan and its latency belong to
  [PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md](PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md).
- **It does not add reporter-side early-reveal monitoring.** Bounty hunting via
  `MsgSlashGuardian` is a revenue feature, not an obligation, and is out of
  scope.
- **It does not persist in-flight state across restarts.** The
  one-duplicate-per-restart cost remains accepted (`timeflareio/chain` —
  `DONE_GUARDIAN_INFLIGHT_SUBMISSIONS_PLAN.md`).
- **It does not correct the in-flight guard's stated rationale.** That is
  [PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md](PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md);
  phase 5 here changes when a reservation expires, and both touch the same
  comment in `inflight.go`.

---

## Open questions

1. **Should a positively-bad share be rejected, or silently skipped?** Skipping
   saves an unreimbursed fee and denies a griefing creator its lever; rejecting
   frees the creator's slot sooner and is more informative to the network.
   *Recommendation: skip by default, with rejection behind an opt-in.* The
   guardian's own economics favour silence, the protocol treats the two
   identically for progression, and a creator who distributed garbage has no
   claim on the guardian's fee. The opt-in exists because a well-behaved
   network benefits from fast rejection, and an operator may reasonably choose
   to pay for that.

2. **Should provisional reveals survive a restart?** Phase 3 makes
   `MarkRevealed` provisional in memory; a restart loses the provisional state
   and re-broadcasts once.
   *Recommendation: accept the re-broadcast.* It is the same
   one-duplicate-per-restart trade already ruled on for the in-flight registry,
   and persisting it would introduce a durable store the daemon does not
   otherwise need.

3. **Wire `max_concurrent_secrets`, or delete it?** The chain already caps
   active bonds at 100.
   *Recommendation: wire it, defaulting to the chain cap.* spec.md assigns
   capacity assessment to the guardian, and an operator with a small float or a
   modest host has a real reason to sit below 100. Deleting it would leave the
   spec's instruction with no mechanism. If it is wired, its validation should
   reject values above the chain cap, since a local cap above the hard one is
   meaningless.

4. **How should repeated float shortfall back off?** Options: a fixed cooldown
   per secret, exponential backoff, or simply stop attempting until float
   changes.
   *Recommendation: stop attempting until observed float or the active bond
   count changes.* It is the cheapest and most honest: nothing about retrying
   improves the odds while the balance is unchanged, and the daemon already
   refreshes both values every cycle.

5. **Should the last-chance reveal attempt be configurable?** An operator
   confident their node never lags might prefer to save the fee.
   *Recommendation: no.* One fee against half a bond is not a trade worth
   exposing, and a knob here is a knob that gets set wrongly once and discovered
   at settlement.

---

## Related plans

- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — the complement: that plan makes a submission survive the network, this one
  decides what to submit and when. Phase 3's inclusion-based success depends on
  submissions not being silently abandoned, which is its phase 2.
- [PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md](PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md)
  — corrects the justification written beside `inFlightExpiryBlocks`, the
  constant phase 5 builds on.
- [PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md](PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md)
  — owns discovery latency, which is the upstream cause of edge-of-window
  reveals.
- [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  — consumes this plan's classified decisions and broadcast-versus-inclusion
  distinction as metrics.
- [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
  — holds `max_concurrent_secrets` on its dead list pending open question 3.
