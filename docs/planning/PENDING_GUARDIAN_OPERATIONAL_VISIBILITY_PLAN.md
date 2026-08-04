# Guardian operational visibility

**Priority**: P1 — an operator cannot currently alert on any condition that
precedes a slash, and the health probe everything is wired to cannot detect the
failure it exists to catch.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
findings 8, 9, 23, 25, 26, 27, 28.
**Components**: `guardian/monitoring/health.go`, `service.go`, `metrics.go`,
`client.go`; `guardian/guardian/service.go`, `events.go`, `observations.go`,
`dashboard_source.go`; `guardian/cmd/guardiand/cmd/health.go`, `start.go`,
`register.go`, `update.go`, `root.go`, `version.go`; `guardian/Dockerfile`;
`make/common.mk`; `devnet/docker/generate-compose.sh`;
`docs/guides/CONTAINERS.md`.

---

## The issue

The daemon computes almost everything an operator needs and exports almost none
of it. Seven defects, grouped by what they cost.

### 1. `/health` cannot detect a wedged daemon, and nothing probes `/ready`

`monitoring/health.go:99-100`: `Healthy = ChainReachable && KeyLoadable`, two
sticky booleans. The staleness bound applies only to `Ready`. A monitoring loop
that wedges leaves both flags at their last value, so `/health` returns 200
indefinitely.

`handleReady`'s own comment says "supervisors restart the guardian"
(`monitoring/service.go:362-364`), but every shipped supervisor hook probes
`/health`:

- `guardiand health` hits `baseURL + "/health"` with no `--ready` option
  (`monitoring/client.go:28`, `cmd/health.go:32-35`);
- the compose healthcheck is `["CMD","guardiand","health",...]`
  (`devnet/docker/generate-compose.sh:162`);
- CONTAINERS.md's recipe likewise.

So the daemon wedges mid-window, Docker health stays green, nothing restarts,
reveals are missed. The mechanism designed to catch this is wired to nothing.

This is also the only existing mitigation for the transaction-path wedge in
[PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
issue 1, which is why it matters twice.

### 2. No metric answers "am I about to be slashed?"

The metric inventory (`monitoring/service.go:34-57`) has no gauge for
unrevealed accepted assignments, blocks to the nearest window close, at-risk
count, float locked versus unlocked, the guardian's bond multiplier `k` (the
per-guardian multiplier pricing each bond), or chain reachability.

`guardian_reveal_windows_missed_total` is labelled the "leading indicator"
(`service.go:103-106`) but increments only *after* the window closes — it is a
lagging indicator of a slash already incurred.

Two registered metrics are never written at all: `guardian_balance`
(`SetBalance`, `metrics.go:84-90`, zero callers) and
`guardian_blockchain_request_duration_seconds` (`service.go:114-118`, no
`Observe`). The balance one matters most — "top up before you cannot cover
reveal fees" is the first alert any operator writes, and it can never fire.

Everything missing is already computed for the dashboard: `revealRisk`
(`dashboard_source.go:216-238`) and `Economics` (`:241-310`, FloatLocked,
FloatUnlocked, BondK, TotalBonded). None reaches `/metrics`.

### 3. Missed-window detection only runs on the event path

`checkMissedWindows` is called only from `onNewHeight` (`service.go:571`); the
polling fallback (`:437-475`) never calls it. With
`enable_event_monitoring: false` — supported, and per `service.go:307` the mode
where polling is "the only path" — the counter never increments and the "Reveal
window closed without our reveal" error log (`:591`) is never produced. The
same silence applies while the WebSocket is down and reconnecting.

The miss signal therefore goes quiet during exactly the connectivity trouble
most likely to cause misses.

### 4. EndBlock events are structurally invisible

`events.go:76-83` subscribes to `tm.event='NewBlockHeader'` and `tm.event='Tx'`
only. Settlement, no-reveal slashes, commit finalisation and
`settlement_stalled` are emitted in EndBlock
(`x/secrets/keeper/endblock_logic.go`; `x/secrets/types/constants.go:169`),
which CometBFT delivers under `NewBlock` — matched by neither subscription.

`Observations.RecordSettlement` consequently has zero production callers, so
the dashboard's settlements panel, including its `stalled` alarm surface
(`dashboard/dashboard.go:283-289`), permanently renders its empty state. An
operator reads that as "no settlements happened".

The daemon learns of its own no-reveal slash only by inference, of an
early-reveal slash against it never, and of bond returns never. spec.md's
settlement-failure design states "the alarm *is* the detection mechanism",
which assumes somebody is listening.

### 5. Missing config exits 0

`runStart` returns nil after `ShowNoConfigMessage` (`start.go:70-74`);
`runRegister` likewise (`register.go:64-69`). `update` correctly returns an
error (`update.go:60-63`).

A systemd unit or `docker run … start --accept` against an empty volume exits
**success**, so `Restart=on-failure` never fires and the guardian is not
running while every probe reports a clean exit. The same applies to `start`
without `--accept` under a supervisor with no TTY: EOF makes
`promptForConfirmation` return false (`cmd/utils.go:13-24`) and it exits 0.

### 6. Registration success is printed on CheckTx alone

Broadcast is `BROADCAST_MODE_SYNC` (`signer.go:239-247`). `rotate-key` handles
this correctly, polling the guardian record before touching local state
(`rotate_key.go:222-243`). `register` does not: `RegisterWithOptions` returns at
broadcast (`registration.go:104-141`) and the CLI prints "✅ Guardian
Registration Successful! … Registered and ready for assignments"
(`register.go:140`, `:205-212`).

A DeliverTx failure — duplicate encryption key, balance short of deposit plus
entry fee — leaves the operator believing they are registered. The 1,000 VEIL
entry fee is not lost (state rolls back; only gas is spent), but nothing tells
them which outcome occurred.

### 7. `guardiand version` is hardcoded and the ldflags are inert

`cmd/root.go:13` declares `version = "1.0.0"` as a constant. The Dockerfile
(`:29-33`) and `make/common.mk:19-24` inject
`-X github.com/cosmos/cosmos-sdk/version.{Version,Commit}` — and the guardian
imports nothing from that package, verified by grep, so the stamp does nothing.

Post-upgrade verification is therefore impossible: `guardiand version` and the
dashboard Vitals panel (same constant via `SetBuildInfo`, `start.go:123`) both
report 1.0.0 forever. `CONTAINERS.md:18-19` claims "Version stamping mirrors
the native build", true only for `timeflared`.

---

## Design

### Phase 1 — make readiness the probe that is used

Two changes, either of which alone would do, and which are better together:

1. `guardiand health` gains `--ready` selecting the `/ready` endpoint, and the
   compose healthcheck plus CONTAINERS.md switch to it.
2. `/health` itself gains a staleness component, so liveness reflects a wedged
   loop rather than only dependency state.

The distinction worth preserving: `/health` should mean "this process is
working", `/ready` "and it is currently doing its job". A wedged monitoring
loop violates the first, not just the second, which is why change 2 matters
even after change 1.

### Phase 2 — export what the dashboard already computes

New gauges, sourced from the existing computations rather than new queries:

| Metric | Source |
|---|---|
| `guardian_balance` | wire the dead `SetBalance` into the poll cycle |
| `guardian_assignments_pending_reveal` | cache `pendingReveal` index size |
| `guardian_blocks_to_nearest_reveal_deadline` | `revealRisk` (`dashboard_source.go:216`) |
| `guardian_assignments_at_risk` | same |
| `guardian_float_locked` / `guardian_float_unlocked` | `Economics` (`:241`) |
| `guardian_bond_multiplier_k` | same |
| `guardian_active_bond_count` | same |
| `guardian_chain_reachable` | `Health.chainReachable` |
| `guardian_key_epoch_current` | key resolver |

Two "how close is a window" gauges exist across the plans and must stay
distinct: `guardian_blocks_to_nearest_reveal_deadline` here measures an
obligation already accepted, while `guardian_availability_blocks_remaining`
([PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
phase 4) measures future eligibility. Conflating them would hide whichever is
less urgent at any moment, which is exactly when the other one matters.

Plus the two dead registrations: write `guardian_blockchain_request_duration_seconds`
from `withRetry`, or remove it (open question 3).

The alerting story this enables should be documented alongside, because a
metric nobody knows to alert on is barely better than no metric.

### Phase 3 — missed-window detection on every path

Call `checkMissedWindows` from `processSecrets` as well as `onNewHeight`, and
crucially *before* `UpdateFromBlockchain` evicts the expired entry — otherwise
the poll path races the same eviction that hides the miss today.

This interacts with
[PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
phase 2: the false-`MarkRevealed` bug currently hides misses from this same
function, so both fixes are needed for the counter to be trustworthy.

### Phase 4 — observe EndBlock events

Add a `tm.event='NewBlock'` subscription and parse the settlement, slashing and
commit-finalisation events for our own address, feeding the already-built
`RecordSettlement` pipeline.

This is the one phase that could grow rather than shrink work, since it means
parsing event attributes the daemon has not needed before. It is also the phase
that makes the daemon able to *report a slash against itself*, which no other
mechanism does. Scope is open question 2.

### Phase 5 — honest exit codes and honest success

- `runStart` and `runRegister` return a non-zero error when configuration is
  missing, matching `update`.
- The non-interactive cancellation path returns non-zero as well; a supervisor
  that cannot answer a prompt has not "successfully declined".
- `register` polls the guardian record after broadcast before declaring
  success, reusing `rotate_key.go:222-243`'s pattern, and reports the
  distinction between "broadcast accepted" and "registration confirmed".

### Phase 6 — real version reporting

Point the ldflags at `-X …/guardiand/cmd.version` and `.commit`, turn the
constant into a variable, and print both. Then fix the CONTAINERS.md claim.

---

## What this plan does not solve

- **It does not add authentication to any of these surfaces.** The new metrics
  make `/metrics` more sensitive, which strengthens the case in
  [DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)
  §6 but does not resolve it. That plan should be told the residual grew.
- **It does not add alerting rules or dashboards.** Exporting the metrics is
  in scope; shipping a Grafana dashboard is not, and would be a new maintenance
  surface needing its own argument.
- **It does not persist observation history.** The capped in-memory buffers and
  their "since process start" framing stand
  ([DONE_GUARDIAN_DASHBOARD_PLAN.md](../done/DONE_GUARDIAN_DASHBOARD_PLAN.md)).
- **It does not fix log retention.** `log_file_path` is dead and belongs to
  [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md),
  though it is squarely an observability gap — an operator with no log file has
  no forensics after a missed reveal.
- **It does not add a drain-readiness signal.** "Is it safe to stop now?" is a
  real operational question with no CLI answer today; see open question 4.

---

## Open questions

1. **Should `/health` absorb the staleness check, or stay dependency-only?**
   Merging simplifies the story to one endpoint; separating preserves the
   liveness/readiness distinction that Kubernetes and similar expect.
   *Recommendation: keep both endpoints, but add staleness to `/health` as
   well, with a longer bound than `/ready`.* Readiness should flap on a brief
   stall; liveness should trip only on a genuine wedge. Two bounds, one
   concept, and the shipped probes stop being wrong either way.

2. **How far should EndBlock observation go?** Minimum: settlement and
   `settlement_stalled` for our own secrets, feeding the existing panel.
   Maximum: full lifecycle observation replacing parts of the poll loop.
   *Recommendation: the minimum, and explicitly not more.* The polling loop
   already covers state progress correctly; the value here is the alarm and the
   slash notification, not a second discovery path. A second discovery path
   would duplicate a concern, which the minimalism rule warns against.

3. **Wire or remove `guardian_blockchain_request_duration_seconds`?**
   *Recommendation: wire it from `withRetry`.* It is a two-line change, and
   chain-request latency is genuinely diagnostic when reveals start landing
   late — it distinguishes "our node is slow" from "we discovered it late".

4. **Should there be a "safe to stop" command or endpoint?** An operator
   upgrading a binary has no CLI way to check whether a reveal window is
   imminent; only the dashboard shows it.
   *Recommendation: add it to `guardiand status` rather than as a new
   endpoint.* The data is already in the cache, `status` is where an operator
   already looks, and it avoids a new surface. It also gives `status --detailed`
   — currently a no-op (finding 39) — something real to do.

5. **Does the balance gauge need a denom label?** The guardian transacts in one
   denom.
   *Recommendation: no label, but name the metric `guardian_balance_uveil` so
   the unit is not ambiguous at the query site.*

---

## Related plans

- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — `/ready` is the only current mitigation for its wedge; phase 1 here is what
  makes that mitigation real.
- [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
  — its phase 2 and this plan's phase 3 are jointly required for the
  missed-window counter to be trustworthy; its broadcast-versus-inclusion
  distinction is what phase 2 exports.
- [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
  — `SetBalance` and `RecordSettlement` are on its dead list precisely because
  this plan is what brings them to life; the two must not race each other.
- [DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)
  — its §6 residual grows with phase 2.
