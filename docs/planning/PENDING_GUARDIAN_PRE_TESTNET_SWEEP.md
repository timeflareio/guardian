# Guardian pre-testnet sweep

**Priority**: P0 — the guardian is the component that gets slashed; findings
below include one remote-ish code-execution path and several routes to a
no-reveal slash on a correctly-operated guardian.
**Status**: refining (31 July 2026) — this is a *findings inventory*, not an
executable plan. Individual plans break out of it (see "How this document is
used").
**Origin**: full sweep of `guardian/` requested 31 July 2026 ahead of testnet
— code, protocol, configuration, operations, custody and dead code.
**Components**: `guardian/` (all packages), `x/secrets/keeper` and
`x/secrets/types` (conformance references only), `proto/timeflare/secrets/v1`,
`docs/operations.md`, `docs/guides/CONTAINERS.md`, `devnet/`,
`docs/CHAIN_MECHANICS.md`.

---

## How this document is used

This is a **sweep**, not a plan. It records every finding that survived
verification, with its receipt, so that individual plans can be cut from it
without re-doing the investigation. Nothing here is executed directly; per
`docs/planning/README.md` a finding becomes work only once it is folded into
a plan the owner has approved.

**Evidence standard applied**: every entry below cites a file and line, and
where the claim is about protocol behaviour it also cites the chain-side code
that enforces it. Candidate findings that could not be proved were dropped
rather than hedged. Items already recorded in
[CHAIN_MECHANICS.md](../../CHAIN_MECHANICS.md) are excluded by
construction, as is work already owned by
[DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md);
where a finding sits *adjacent* to an accepted trade-off, the entry says which
one and why it is not covered by it.

**What this sweep does not cover**: the chain module's own correctness, the
TypeScript SDK, the Rust crypto crate, and the economics themselves. It covers
the guardian daemon's implementation of them.

---

## Severity summary

| # | Finding | Severity | Class |
|---|---|---|---|
| 1 | Backup-bundle restore writes outside the keyring directory (path traversal) | **Critical** | Security |
| 2 | Transient key unavailability causes a permanent on-chain rejection | **High** | Protocol |
| 3 | No deadline on the transaction path; one hung call wedges the daemon | **High** | Code |
| 4 | Malformed `gas_price` panics the daemon at its first transaction | **High** | Config |
| 5 | `guardiand start` never validates its configuration | **High** | Config |
| 6 | Chain gRPC is unconditionally plaintext, with no TLS option | **High** | Security |
| 7 | Every poll tick and every secrets transaction rescans the whole chain | **High** | Protocol |
| 8 | `/health` cannot detect a wedged daemon, and nothing probes `/ready` | **High** | Operations |
| 9 | No metric answers "am I about to be slashed?" | **High** | Operations |
| 10 | `guardiand config list` displays nothing at all | **High** | Config |
| 11 | `config set` bakes transient env overrides into the config file | **High** | Config |
| 12 | Window-edge off-by-one: reveals broadcast that can only execute late | Medium | Protocol |
| 13 | A dropped reveal near the window tail is unrecoverable | Medium | Protocol |
| 14 | `ProcessReveal`'s nil return is overloaded; cache marks false reveals | Medium | Code |
| 15 | Shutdown cancels in-flight submissions instead of draining them | Medium | Code |
| 16 | No economic self-assessment at acceptance | Medium | Protocol |
| 17 | `enable_hmac_validation: false` guarantees a bond slash | Medium | Protocol |
| 18 | Data race on shared `*CachedSecret` | Medium | Code |
| 19 | Rotation cutover is two non-atomic renames with no crash recovery | Medium | Security |
| 20 | No ceiling on the simulated gas limit | Medium | Security |
| 21 | Passphrases accepted as command-line arguments | Medium | Security |
| 22 | Node's chain-id is never asserted against the configured one | Medium | Security |
| 23 | Missed-window detection only runs on the event path | Medium | Operations |
| 24 | Single chain endpoint, no failover | Medium | Protocol |
| 25 | EndBlock events are structurally invisible | Medium | Protocol |
| 26 | Missing config exits 0 from `start` and `register` | Medium | Operations |
| 27 | Registration success is printed on CheckTx alone | Medium | Operations |
| 28 | `guardiand version` is hardcoded; ldflags stamp an unread package | Medium | Operations |
| 29 | Custom `--config-path` splits the signing keyring | Medium | Config |
| 30 | Keyring passphrase file is decoded by base64 guess | Medium | Config |
| 31 | `docs/operations.md` is materially stale | Medium | Docs |
| 32 | CONTAINERS.md ships a healthcheck that cannot work on distroless | Medium | Docs |
| 33 | Explicit rejection pays an unreimbursed fee where silence is free | Low–Med | Protocol |
| 34 | Validation gaps on numeric and duration fields | Low–Med | Config |
| 35 | No withdraw verb in `guardiand` | Low–Med | Operations |
| 36 | Recovery mnemonic is read with terminal echo | Low | Security |
| 37 | `register --accepting-secrets=false` is silently ignored | Low | Protocol |
| 38 | No availability-window lifecycle management | Low | Protocol |
| 39 | Dead code, dead config and dead flags | Low | Hygiene |
| 40 | Key-material hygiene gaps | Low | Security |
| 41 | Miscellaneous correctness and copy defects | Low | Hygiene |
| 42 | Two plans marked `done` have undelivered deliverables | — | Process |

---

## 1. Critical — backup-bundle restore writes outside the keyring directory

`guardian/custody/bundle.go:200-217`. `RestoreKeyringFiles` joins each map key
from the bundle onto `keyringDir` and writes it:

```go
for rel, content := range files {
    target := filepath.Join(keyringDir, rel)
    if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil { ... }
    if err := os.WriteFile(target, content, 0600); err != nil { ... }
}
```

`rel` is attacker-controlled data from inside the bundle
(`Bundle.KeyringFiles`, `bundle.go:45`). `filepath.Join` cleans a path but does
not contain it, and `Bundle.Validate()` (`bundle.go:58-85`) checks versions and
key lengths — never the map keys. Verified:
`filepath.Join("/home/g/.timeflare/guardian", "../../.ssh/authorized_keys")`
resolves to `/home/g/.ssh/authorized_keys`.

**Why it matters**: the write lands as the guardian user, which owns
`private_key`, every `private_key.epoch<N>`, both passphrase files and the
signing keyring. A hostile bundle presented to an operator as a "recovered
backup" therefore converts one `guardiand key restore` into full custody
compromise. The existence guard at `bundle.go:203` blocks overwrites but not
creations — and `--force` (`key.go:251`, a documented restore flag) disables
even that.

Containment must be checked in `Validate()` so a malformed bundle fails at
`OpenBundle` rather than mid-write, with a table test covering `../`, absolute
and `a/../../b` keys.

---

## 2. High — transient key unavailability causes a permanent on-chain rejection

`guardian/guardian/reveal.go:109-121`: **any** decrypt failure — including
`ErrPrivateKeyUnavailable`, returned at `reveal.go:351-352` when no epoch key
loads — falls through to `rejectAssignment`.

The daemon contradicts itself here. Startup deliberately tolerates an
unloadable key so that "an operator can attach the passphrase file without a
crash loop" (`service.go:155-175`, `269-276`; the key-custody plan's ruling).
But the confirmation path consults no such health state, so during exactly that
grace period every arriving assignment is **rejected on chain**.

Rejection is final. Verified chain-side at
`x/secrets/keeper/msg_server_confirm_shares.go:65-69` — any status other than
`PROPOSED` returns `ErrAlreadyResponded` — and spec.md "Guardian Acceptance
(Phase 3)": "Decision finality means acceptance responses cannot be changed
once submitted."

**Testnet scenario**: a guardian container restarts and its passphrase secret
mounts a minute late; a Phase-2 distribution lands in that minute; the next
poll tick permanently forfeits the assignment and pays an unreimbursed reject
fee. Nothing the operator can do afterwards recovers it.

**Fix shape**: reject only on *positive* evidence of a bad share (an HMAC
mismatch after a successful decrypt). On key unavailability, stay silent and
retry — the commit window is 20–200 blocks
(`x/secrets/types/constants.go:107-110`) and non-response is a first-class
protocol outcome per spec.md Phase 3.

---

## 3. High — no deadline on the transaction path; one hung call wedges the daemon

`guardian/blockchain/signer.go:164-175`. `SubmitTx` takes the per-account mutex
and holds it across three unbounded network calls: account/sequence retrieval
(`signer.go:181`), gas simulation (`signer.go:216`) and broadcast
(`signer.go:239`). The context it receives is the daemon's **root** run
context — there is no deadline anywhere on the path:

`service.go:437 processSecrets(ctx)` → `processReveals` →
`reveal.go:194 GuardianRevealShare(ctx, …)` → `client.go:354-361` →
`signer.SubmitTx(ctx, msg)`.

`withRetry` (`client.go:76-106`) bounds every *query* by `RequestTimeout`
(30 s default), but none of the five transaction methods
(`client.go:294-361`) route through it. The connection is dialled with
defaults and no keepalive (`client.go:41`), so a half-open socket — node
freeze, NAT drop with no RST — blocks a broadcast for the OS TCP
retransmission timeout, on the order of fifteen minutes.

**Cascade**: the blocked worker holds `s.mu`; every other reveal worker blocks
on it; `processReveals`'s `wg.Wait()` (`service.go:561`) blocks its caller —
including `onNewHeight`, which runs *synchronously* inside the event monitor's
select loop (`events.go:98`), so header processing stops too. Every reveal
window open during that period is a no-reveal slash.

Partial existing mitigation: `/ready` trips after
`max(3 × polling_interval, 30 s)` of no activity (`monitoring/health.go:100`)
— but see finding 8, nothing probes `/ready`.

---

## 4. High — malformed `gas_price` panics the daemon at its first transaction

`guardian/blockchain/signer.go:64-66` swallows the parse error with the comment
`// Validate() owns malformed configuration; this is advisory only`.
`config.Validate()` (`config/config.go:195-291`) contains **no** `gas_price`
check. `signer.go:197` then calls `WithGasPrices(s.cfg.GasPrice)`, and
cosmos-sdk v0.53.4's `client/tx/factory.go:190-194` **panics** on a parse
failure.

**Testnet scenario**: `config set gas-price 0.1` (denom omitted) passes set,
load, validate, doctor and startup. The daemon panics the first time it tries
to confirm or reveal — precisely when an assignment is at stake — and under
systemd crash-loops through the whole commit or reveal window.

Related unchecked cases on the same field: the `gas_price` denom is never
cross-checked against `denom`, and a price *below* the consensus floor
(`x/secrets/types/constants.go:357-358`) is not flagged at startup, so every
transaction fails in CheckTx and the operator discovers it one transaction at
a time.

---

## 5. High — `guardiand start` never validates its configuration

`cmd/guardiand/cmd/start.go:69-137` contains no `Validate()` call;
`root.go:51-63` only does `LoadOrDefault`. The sole callers of `Validate()`
are `config validate`, `config doctor` and the dashboard display.

Set-time checks are type-parse only (`config/registry.go:102-111`), so
`guardiand config set polling-interval 0s` succeeds. `config validate` rejects
it — but `start` never asks, and `service.go:413` then executes
`time.NewTicker(0)`, which **panics**, after the pre-flight checks have already
passed.

Every cross-field invariant `Validate()` exists to enforce — zero intervals,
port collisions, `polling_interval < block_time/2` — sails into a running
daemon. A crash after startup on a guardian holding accepted assignments is a
no-reveal slash.

---

## 6. High — chain gRPC is unconditionally plaintext, with no TLS option

`guardian/blockchain/client.go:41` and `cmd/guardiand/cmd/key.go:437` both
hard-code `grpc.WithTransportCredentials(insecure.NewCredentials())`.
`config.Config` has no TLS field, no CA path and no toggle, so a remote
endpoint cannot be secured even when the operator wants it. The RPC/WebSocket
side (`events.go:65`) has the same property.
`docs/guides/CONTAINERS.md:183` documents exactly the exposed case
(`-e GUARDIAN_GRPC_ENDPOINT=my-node:9090`).

Everything security-relevant crosses this channel: the gas simulation the
guardian signs against, the guardian record the startup key-binding check
compares to, the key-epoch history that selects the decryption key, and the
outbound signed transactions.

**Load-bearing impact — fee drain.** `submitLocked` takes the gas figure
straight from the wire with no ceiling (`signer.go:216-223`). A forged
`SimulateResponse` with a huge `gas_used` is multiplied by `gas_adjustment`
and signed as the declared limit. No `max_gas` is configured anywhere in the
repo, so the Cosmos genesis default of `-1` applies and the only bound is the
account balance; the ante handler deducts the declared fee up front. The
guardian is then unable to cover reveal fees, converting the drain into no-reveal
slashes on every in-flight bond.

Secondary legs are self-limiting (the real chain rejects the resulting
messages, and `decryptShare` has a trial-decrypt fallback), which is why the
drain is the finding.

---

## 7. High — every poll tick and every secrets transaction rescans the whole chain

`guardian/blockchain/client.go:187-221`. `ListSecretsForGuardian` paginates the
**global** `Secrets` query at 200 per page and filters client-side. It is
called by `UpdateFromBlockchain` on every poll tick (6 s default) *and* by
`onChainEvent` → `processSecrets` on **any** transaction carrying a
`secret_*`/`assignment_*`/`guardian_*` event from **anyone**
(`events.go:101-123`, `service.go:576-582`).

Each returned `SecretView` is expensive: it carries every guardian's
`encrypted_share` bytes and assignment records
(`proto/timeflare/secrets/v1/query.proto:108-148`), assembled server-side from
side stores. The chain offers `SecretsByCreator` but **no per-guardian query**
(`query.proto:18-98`), so the protocol gives a guardian no cheaper way to ask
"what involves me?".

Three compounding problems:

- **Load is O(guardians × secrets × transaction-rate)**. Fifty guardians and a
  few thousand live secrets means every accept by any guardian causes all fifty
  daemons to re-page the entire secret set with full share bytes.
- **No coalescing**: a block containing twenty secrets transactions delivers
  twenty channel events and therefore twenty sequential full-store scans, on
  top of the poll doing the same.
- **Backpressure inverts into churn**: the callbacks run synchronously in the
  subscription select loop (`events.go:88-108`), so a multi-second rescan backs
  up the 16/64-slot channels, CometBFT drops the slow subscriber, and the
  monitor reconnects — under exactly the load that caused it.

The approved design in
[DONE_GUARDIAN_IMPROVEMENTS_PLAN.md](../done/DONE_GUARDIAN_IMPROVEMENTS_PLAN.md)
§7.2 accepted client-side filtering, but costed it as "one cheap query per new
secret, network-wide" (:472). The implementation resyncs the full list per
event, which is not what was ruled. Two independent remedies exist: coalescing
events inside the accepted design (cheap, no protocol change), and a
`SecretsByGuardian` query (a protocol-surface change requiring its own approval
and spec update — §7.2 already logged this as §9.5).

Node degradation from this feeds directly into reveal timing, so it amplifies
finding 24.

---

## 8. High — `/health` cannot detect a wedged daemon, and nothing probes `/ready`

`monitoring/health.go:99-100`: `Healthy = ChainReachable && KeyLoadable` — two
sticky booleans. The staleness bound applies only to `Ready`. A monitoring loop
that wedges leaves both flags at their last value, so `/health` returns 200
indefinitely.

The comment on `handleReady` says "supervisors restart the guardian"
(`monitoring/service.go:362-364`) — but every shipped supervisor hook probes
`/health`:

- `guardiand health` hits `baseURL + "/health"` with no `--ready` option
  (`monitoring/client.go:28`, `cmd/health.go:32-35`),
- the compose healthcheck is `["CMD","guardiand","health",...]`
  (`devnet/docker/generate-compose.sh:162`),
- CONTAINERS.md's recipe likewise.

**Incident**: the guardian wedges mid-window, Docker health stays green, no
restart happens, reveals are missed, bonds are slashed. The mechanism designed
to catch this is wired to nothing.

---

## 9. High — no metric answers "am I about to be slashed?"

The full metric inventory (`monitoring/service.go:34-57`) has no gauge for
unrevealed accepted assignments, blocks to the nearest window close, at-risk
count, float locked versus unlocked, the guardian's bond multiplier `k` (the
per-guardian multiplier that prices each bond), or chain reachability.

`guardian_reveal_windows_missed_total` is documented as the "leading indicator"
(`service.go:103-106`) but only increments **after** the window has closed —
it is a lagging indicator of a slash already incurred.

Two registered metrics are never recorded at all:

- **`guardian_balance`** — `SetBalance` (`monitoring/metrics.go:84-90`) has
  zero callers. The first alert any operator writes ("balance low, top up
  before reveal gas fails") can never fire. The dashboard computes
  `SigningBalanceLow` (`dashboard_source.go:303-305`), but that is a human
  page, not an alerting surface.
- **`guardian_blockchain_request_duration_seconds`** — registered at
  `service.go:114-118`, never observed.

The daemon already computes everything missing, for the dashboard:
`revealRisk` (`dashboard_source.go:216-238`) and `Economics`
(`:241-310`, FloatLocked/Unlocked, BondK, TotalBonded). None of it reaches
`/metrics`. An operator running the Prometheus/Grafana stack the docs
themselves recommend cannot page on any condition that precedes a slash.

---

## 10. High — `guardiand config list` displays nothing at all

`cmd/guardiand/cmd/config.go:846-858` hardcodes group names — `"Network
Configuration"`, `"Staking & Economics"`, `"Networking & Timeouts"`,
`"Registration Defaults"` and others — that match **zero** of the registry's
actual tag groups. The real groups, from the `group:` struct tags in
`config/config.go`, are `Network`, `Identity & Keys`, `Economics`, `Chain
Interaction`, `Service`, `Event Monitoring` and `Monitoring`. Every lookup
misses and is skipped.

Empirically verified against a freshly initialised config: the output is a
header, the config path and a footer, with no settings between them.

This is the command every flow points operators at — `config init`'s next
steps, `config set`'s help, the no-config banner. `config doctor` works because
it iterates `config.GroupOrder()` (`config.go:171`) instead. No test covers the
`cmd` package, which is how it drifted.

---

## 11. High — `config set` bakes transient env overrides into the config file

`cmd/guardiand/cmd/config.go:787-808` — `runConfigSet` does
`Load()` → `Set` → `Save()`. `Load` applies `ApplyEnvOverrides` into the
in-memory config (`config/manager.go:66`), and `Save` serialises the
**effective** values (`manager.go:86-118`). The same pattern appears in
`runConfigMigrateKey` (`config.go:1097`, `1106`).

Empirically verified:
`GUARDIAN_POLLING_INTERVAL=99s guardiand config set retry-attempts 5` wrote
`polling_interval: 1m39s` permanently into the file.

This inverts the precedence the generated YAML header itself documents —
"flags > env > file > defaults" (`manager.go:107`). An operator who runs any
`config set` inside a shell or unit that exports a `GUARDIAN_*` variable — say
a temporary `GUARDIAN_RPC_ENDPOINT` pointing at a fallback node — silently
promotes that transient override into permanent configuration.

---

## 12. Medium — window-edge off-by-one on both reveal and confirm

`cache.go:208-215` and `reveal.go:257-273` permit a reveal while
`currentHeight <= RevealEndBlock`. A transaction broadcast when the
**committed** height is `end` is included at `end + 1` at the earliest, and
`x/secrets/keeper/reveal_window.go:30-33` rejects `currentHeight >
RevealEndBlock`. So a broadcast at observed `H == end` passes simulation and
CheckTx (both evaluated against state at `end`) and then deterministically
fails in DeliverTx — the fee is spent for nothing.

Worse, the daemon then believes it revealed: `service.go:557-559` calls
`MarkRevealed` on broadcast success, and `metrics.RecordReveal(true, …)` fires
(`reveal.go:209-210`). Telemetry reports success while a no-reveal slash
follows at settlement.

The confirm side has the same shape and is blinder still: `processConfirmation`
never looks at `CommitDeadline` at all — the field is used only by the
dashboard (`dashboard_source.go:152-180`) — while
`msg_server_confirm_shares.go:43-48` rejects past-deadline confirms.

**Fix shape**: the last safe broadcast height is `end − 1` (and
`commit_deadline − 1`), and reveal success should key off inclusion rather
than CheckTx.

---

## 13. Medium — a dropped reveal near the window tail is unrecoverable

A reveal that passes CheckTx sets `MarkRevealed` (`service.go:558`). If the
transaction is then dropped from the mempool — node restart, mempool eviction —
chain state never shows it. The recovery path is: the next poll preserves
`StateRevealed` (`cache.go:437-449`), `shouldEvict` evicts it
(`cache.go:218-221`), and the tick after that re-adds it as `StateNeedsReveal`.
But `InFlightRegistry.Reserve` then refuses resubmission until
`broadcastHeight + 5` (`inflight.go:26-35`, `:80-84`).

`inflight.go:33-35` names this tension itself: the expiry "must also stay under
the reveal path's evict-and-re-add recovery, or the guard would turn a case
that self-corrects today into a missed reveal window — which costs 50% of the
bond".

**Scenario**: congestion pushes the first successful broadcast to `end − 3`;
the node restarts and flushes its mempool; retry is blocked until `end + 2`.
The window closes and the guardian is slashed despite fully correct operation.
With `MinRevealDuration = 100` blocks
(`x/secrets/types/constants.go:94-95`), the exposed tail is 5–7% of the
smallest permitted window.

**Fix shape**: when re-adding a secret whose window has fewer than
`inFlightExpiryBlocks` remaining, force-release the stale reservation. The
chain rejects true duplicates for one fee — cheap insurance against losing half
a bond.

---

## 14. Medium — `ProcessReveal`'s nil return is overloaded

`service.go:546-559` treats `err == nil` as "revealed" and calls
`MarkRevealed`. But `reveal.go:143-166` returns nil in three non-submission
cases: `shouldRevealNow` false because the jitter offset has not arrived,
`shouldRevealNow` false because the window has already passed, and `Reserve`
false because a submission is already in flight.

Consequences:

- With `reveal_offset_blocks > 0`, an in-window secret that is merely not due
  yet cycles mark-revealed → evict → re-add on every poll, and the actual
  reveal lands up to roughly two polling intervals after its planned height.
  The planned height is capped at half the window (`reveal.go:291`) so it still
  fits, but the safety margin is consumed by cache churn, invisibly.
- When the window has already passed, the nil → `MarkRevealed` path flips state
  to `StateRevealed`, so `checkMissedWindows` — which filters on
  `StateNeedsReveal` (`service.go:588`) — can fail to count the miss. The slash
  indicator under-reports in exactly the case it exists for.
- Correctness now depends on `shouldEvict` evicting `StateRevealed`. A future
  cache change that breaks that assumption converts this into a permanent
  missed reveal.

**Fix shape**: return a tri-state (submitted / skipped / error) and
`MarkRevealed` only on submitted.

---

## 15. Medium — shutdown cancels in-flight submissions instead of draining them

`cmd/guardiand/cmd/start.go:184-190`: on SIGTERM, `cancel()` fires **before**
`GracefulShutdown`. That run context is the one flowing into `BroadcastTx`, so
a reveal mid-broadcast is aborted at the gRPC layer rather than completed.

`Service.Stop` (`service.go:320-334`) then merely flips `isRunning` — it waits
for neither `runSecretMonitoring`, nor the reveal worker pool, nor the event
monitor — so `GracefulShutdown` returns "clean" immediately and the process
exits with workers possibly mid-flight. The 30 s `ShutdownTimeout` is consumed
only by the HTTP servers (`monitoring/service.go:306-312`).

**Scenario**: an operator restarts for a binary upgrade just as a window opens;
the broadcast is cancelled between signing and mempool acceptance. Recovery
depends on the restart completing while the window is still open — acceptable
for wide windows, a real slash risk for minimum-width ones.

---

## 16. Medium — no economic self-assessment at acceptance

`processConfirmation` (`reveal.go:88-140`) accepts on exactly two conditions:
the share decrypts and the HMAC verifies. It never consults unlocked float, the
frozen bond, distance, or the active bond count — yet records
`Reason: "share HMAC verified, bond affordable"` (`reveal.go:378-381`). The
log asserts a check that does not exist.

The data is already fetched and available — `Secret.OurBondUveil`/`BondFor` and
`Guardian.Stake`/`LockedStake`/`BondK`/`ActiveBondCount`
(`blockchain/types.go:21-29`, `:69-86`) — but is used only by the dashboard
(`cache_snapshot.go:97`).

The chain is the hard gate, so the daemon cannot over-commit its float:
`msg_server_confirm_shares.go:94-103` fails the accept when unlocked float is
below the frozen bond `B_g`, or when the guardian is at the
`MaxActiveBondsPerGuardian` cap of 100
(`x/secrets/types/constants.go:245-249`). But when short, the daemon retries
the doomed accept **every poll tick until the commit deadline**, with no
classification and no backoff, and the operator sees only generic errors.

spec.md Phase 3 explicitly assigns this responsibility to the daemon:
"Capacity assessment is otherwise the guardian's own responsibility — its
client should decline assignments beyond what its infrastructure can serve."
There is no such policy at all; every decryptable assignment is accepted,
including a maximum-distance, maximum-`bump` bond that locks float for a year.
The `max_concurrent_secrets` knob that would express it is dead (finding 39).

---

## 17. Medium — `enable_hmac_validation: false` guarantees a bond slash

`reveal.go:225-229` skips validation entirely, at **debug** level, and
`Config.Validate()` says nothing about the flag.

Acceptance is what locks the bond, and the HMAC is the only thing standing
between "I decrypted something" and "I hold the share the chain will check at
reveal". With it off, the guardian bonds against unverified data; at reveal,
`x/secrets/keeper/msg_server_reveal_share.go:84-86` rejects any share whose
recomputed HMAC mismatches — deterministically, on every retry — so the window
closes into a no-reveal slash: 40% of the bond burned, 10% to the creator, 50%
returned, excluded from the reward pool, and `k` multiplied by 1.26.

spec.md Phase 3 makes offline HMAC verification a *requirement* of accepting.
The only cost of the check is one SHA-256. Given the architectural-minimalism
stance, removing the flag is cleaner than documenting it.

---

## 18. Medium — data race on shared `*CachedSecret`

`cache.go:420-478` — `updateSecretUnsafe` reassigns `cached.Secret`,
`cached.Assignment`, `cached.LocalState` and `cached.LastUpdated` under
`cache.mu`. But the accessors hand out the live pointers, and several daemon
paths read those fields with no lock held:

- `service.go:487-500 reconcileInFlight` reads `c.Assignment.Status` and
  `c.Secret.HasRevealed(...)` from a `GetAll()` copy — the map is copied, the
  `*CachedSecret` values are shared;
- `service.go:586-597 checkMissedWindows` reads `LocalState` and
  `Secret.RevealEndBlock`;
- `service.go:539-560 processReveals` workers read `Secret`/`Assignment` after
  the cache lock was released.

Two writers genuinely run concurrently: the polling loop and the event-monitor
callbacks, with no coalescing or mutual exclusion between them. A reader can
observe a torn pair — a new `Secret` with an old `Assignment`, or a stale
`LocalState`.

Practical damage is bounded today (`EncryptedShare` is immutable per assignment
and the in-flight registry gates duplicate submissions), but it is a genuine
memory-model violation in the hottest path, and the existing race suite does
not cover it because the tests never run the poll loop and event monitor
together.

The irony: `cache_snapshot.go:9-15` documents this precise hazard and fixes it
for the *dashboard*, while the daemon's own consumers still read live pointers.

---

## 19. Medium — rotation cutover is two non-atomic renames with no crash recovery

`cmd/guardiand/cmd/rotate_key.go:245-257` performs the cutover as two renames:
current key → `.epoch<N>`, then `.next` → current. The surrounding ceremony is
the best-designed flow in the module — bundle written first, on-chain record
confirmed to have advanced past CheckTx before any local file is touched
(`:224-243`), staged key pre-encrypted so the cutover "cannot prompt or fail on
encryption" (`:186-188`). The residual is the window *between* the two renames.

**Crash state**: a SIGKILL, OOM or power loss between `:248` and `:253` leaves
**no file at the configured private-key path** — the old key sits at
`.epoch<N>`, the new key at `.next`, and the chain has already advanced. On the
next start, `GetEncryptionPrivateKey()` fails, so `verifyShareKeyBinding`
returns nil at `service.go:161` (unloadable is deliberately a health signal),
and `verifyEpochKeyring` hard-fails only on a missing *retired* epoch — so
neither guard catches it. The daemon runs unhealthy, decrypts nothing, and
misses every window until a human notices.

Nothing reclaims `.next`: it appears exactly once in the module, at the line
that creates it. There is no startup detection and no `rotate-key --resume`;
the recovery instructions live only inside error-return paths (`:249-256`),
which a hard crash never reaches.

Two adjacent edges: `SaveEncryptedShareKey` (`custody/keyfile.go:84-91`) does
`WriteFile(tmp) → Rename` with no `f.Sync()` and no directory sync, so the
rename is atomic for visibility but not durability; and a declined rotation
leaves the bundle containing the freshly generated private key on disk
(`:173`, `:180`) with nothing deleting it.

This sits beside — but is not covered by — CHAIN_MECHANICS.md Trade-off §14
(rotation is hygiene, not recovery), which accepts that a *lost* key is
unrecoverable, not that the rotation ceremony may lose it.

---

## 20. Medium — no ceiling on the simulated gas limit

`signer.go:216-223` declares `max(simulated, reimbursed)` with no upper bound.
CHAIN_MECHANICS.md Trade-off §17 accepts that declared gas exceeds consumed gas and that
the daemon declares the larger simulated figure where a handler outgrows
reimbursement — but §17's reasoning assumes an *honest* simulation. It does not
accept an unbounded figure.

One bad simulation — hostile node, MITM per finding 6, or a chain-side
regression — costs the entire signing balance in a single transaction, which
then converts into no-reveal slashes across every in-flight bond. Measured
consumption is ~89.5k and ~95.5k gas against 120k/130k declared
(`signer.go:36`), so a ceiling three orders of magnitude above that is still
generous. This is the defence that holds even when TLS is misconfigured.

---

## 21. Medium — passphrases accepted as command-line arguments

`cmd/guardiand/cmd/config.go:308-309` — `config init --keyring-passphrase` and
`--encryption-key-passphrase` take the **secrets themselves** as argv,
consumed literally at `config.go:428-429`, `:526-527`, `:590-596`. The
command's own `Example` block teaches the pattern (`:280-298`), and both
devnet scripts use it (`devnet/guardians.sh:172-173`,
`devnet/docker/init-guardians.sh:98-99`).

Both land in shell history, `ps` output and `/proc/<pid>/cmdline`. Together
they are the entire at-rest defence — the custody guide is explicit that
encryption is what protects "stolen disks, leaked snapshots, mis-scoped
backups".

This is the one place the codebase contradicts a rule it has already written
down. The config *fields* of the same names hold file paths by design
(`config.go:60`, `:63`), the container env vars carry paths not secrets, and
the dashboard-auth plan states the principle outright: "It never accepts the
password as an argument, because arguments land in shell history and `ps`."
The `config init` flags predate that rule and were never brought in line.
Note also the same-name/different-semantics trap: the flag takes a secret while
the identically named config key takes a path.

---

## 22. Medium — the node's chain-id is never asserted against the configured one

`signer.go:150`, `:189` bind `WithChainID(s.cfg.ChainID)`; nothing anywhere
verifies it against the node. `Ping` (`client.go:109-112`) only fetches the
block height, so with a wrong `chain_id` the daemon starts, queries succeed and
health goes green.

Every signed transaction is then rejected for signature failure. There is no
cross-chain replay exposure — SIGN_MODE_DIRECT binds chain-id, account number
and sequence — the risk is silent, total economic failure: a guardian whose
chain-id is wrong *after* bonds are locked misses every reveal window and is
slashed on all of them.

This is the same class the codebase already defends against: `verifyShareKeyBinding`
(`service.go:158-175`) refuses to run against the wrong share key precisely
because "running with the wrong key means missing every reveal". The chain-id
has an identical failure mode and no equivalent guard, despite
`cmtservice.ServiceClient` already being held at `client.go:31`.

---

## 23. Medium — missed-window detection only runs on the event path

`checkMissedWindows` is called only from `onNewHeight` (`service.go:571`). The
polling fallback (`processSecrets`, `:437-475`) never calls it.

With `enable_event_monitoring: false` — a supported configuration, and per
`service.go:307` the mode where polling is "the only path" — the
`windows_missed` counter never increments and the "Reveal window closed without
our reveal" error log (`:591`) is never produced. The same silence applies
while the WebSocket is down and reconnecting.

So the miss counter and its log line go quiet during exactly the connectivity
trouble most likely to cause misses. Even with events on, a poll tick that
refreshes the cache first can evict the expired entry before the next header
arrives, skipping the metric on timing alone.

---

## 24. Medium — single chain endpoint, no failover

One `GRPCEndpoint` and one `RPCEndpoint` (`config/config.go:133-134`) carry all
queries, broadcasts and subscriptions. Height-query failure degrades to
`lastKnownHeight` (`service.go:440-453`), which keeps reveals moving only if
broadcast still works — that is, not when the node itself is down or has forked
off.

Settlement grants no liveness excuse: per spec.md "Settlement
(Threshold-Independent)", anything but an HMAC-verified in-window reveal is a
no-reveal. If the guardian's node stalls at `start − 10` while the network
crosses `end`, the daemon sees a window that never opens and
`checkMissedWindows` reports the slash only after recovery. For a daemon whose
entire economic model is "never miss a window", endpoint redundancy is the
cheapest available insurance.

---

## 25. Medium — EndBlock events are structurally invisible

`events.go:76-83` subscribes only to `tm.event='NewBlockHeader'` and
`tm.event='Tx'`. Settlement, no-reveal slashes, commit finalisation and
`settlement_stalled` are all emitted in EndBlock
(`x/secrets/keeper/endblock_logic.go`; `x/secrets/types/constants.go:169`),
which CometBFT delivers under `NewBlock` — matched by neither subscription.

Consequently `Observations.RecordSettlement` (`observations.go:136-137`) has
**zero** production callers, so the dashboard's settlements panel — including
its `stalled` alarm surface (`dashboard/dashboard.go:283-289`) — is permanently
empty, and an operator reads it as "no settlements happened".

The daemon learns of its own no-reveal slash only by inference, of an
early-reveal slash against it never, and of bond returns never. The polling
loop covers state progress, so this is observability plus up-to-one-tick
latency rather than direct slashing exposure — but it is half-built, and the
spec's alarm design (spec.md "Settlement failure handling": "the alarm *is* the
detection mechanism") assumes somebody is listening.

---

## 26. Medium — missing config exits 0 from `start` and `register`

`runStart` returns `nil` after `ShowNoConfigMessage` (`start.go:70-74`);
`runRegister` likewise (`register.go:64-69`). `update` correctly returns an
error (`update.go:60-63`) — the three are inconsistent.

A systemd unit or `docker run … start --accept` against an empty volume
therefore exits **success**: `Restart=on-failure` never triggers and the
guardian is simply not running while every process-manager probe says it exited
cleanly. Related: `start` without `--accept` under a supervisor with no TTY
reads EOF, `promptForConfirmation` returns false (`cmd/utils.go:13-24`), and it
prints "Service startup cancelled" — also exiting 0.

---

## 27. Medium — registration success is printed on CheckTx alone

Broadcast is `BROADCAST_MODE_SYNC` — CheckTx only (`signer.go:239-247`).
`rotate-key` knows this and polls the guardian record before touching local
state (`rotate_key.go:222-243`: "NEVER cut the local key files over until the
on-chain record has actually advanced"). `register` does not:
`RegisterWithOptions` returns at broadcast (`registration.go:104-141`) and the
CLI immediately prints "✅ Guardian Registration Successful! … Registered and
ready for assignments" (`register.go:140`, `:205-212`).

A DeliverTx failure — duplicate encryption key, balance short of deposit plus
entry fee — leaves the operator believing they are registered; they discover
otherwise only via `start`'s pre-flight or `status`. The 1,000 VEIL entry fee
is *not* lost on a DeliverTx failure (state rolls back, only gas is spent), but
nothing tells the operator which outcome occurred. `update` is at least worded
honestly ("Broadcast Successfully").

---

## 28. Medium — `guardiand version` is hardcoded; the ldflags stamp an unread package

`cmd/root.go:13` declares `version = "1.0.0"` as a constant, printed by
`runVersion` (`cmd/version.go:23-27`). The Dockerfile (`guardian/Dockerfile:29-33`)
and `make/common.mk:19-24` inject
`-X github.com/cosmos/cosmos-sdk/version.{Version,Commit}` — and guardiand
imports nothing from that package, so the stamp is inert.

Post-upgrade verification ("is the new binary actually running?") is therefore
impossible via `guardiand version` or the dashboard Vitals panel, which is fed
the same constant through `SetBuildInfo` (`start.go:123`). Both report 1.0.0
forever. `CONTAINERS.md:18-19` claims "Version stamping mirrors the native
build", true only for `timeflared`. Native and Docker builds are at least
consistently wrong in the same way.

---

## 29. Medium — custom `--config-path` splits the signing keyring

`config/manager.go:177-197` — `GetKeyDirectory()` returns the *config file's*
directory for custom paths, and `config init` prints
`timeflared keys add … --keyring-dir <that dir>` from it
(`cmd/config.go:493-501`), placing the encryption keys there too. But the
runtime keyring opens **`cfg.KeyringDir`** (`blockchain/keys.go:126`), which —
unless `--keyring-dir` was explicitly passed — remains the default
`~/.timeflare/guardian`, and that default is what `Save()` writes into the
YAML.

So an operator who runs `guardiand config init --config-path
/etc/guardian/config.yaml`, then creates the signing key exactly where the tool
told them to, gets `ErrKeyNotFound` from `register` and `start`. The devnet
dodges it only because `guardians.sh:171-173` always passes `--keyring-dir`.

---

## 30. Medium — keyring passphrase file is decoded by base64 guess

`blockchain/keys.go:97-107` — `readPassphraseFile` attempts a base64 decode and
uses the result "when possible", falling back to the raw text. The config field
is documented simply as "Path to a file containing the keyring passphrase"
(`config/config.go:63`).

An operator who writes a raw passphrase — the natural reading of that
description — whose length is a multiple of four over the base64 alphabet
(`correcthorse`, `Passw0rd`, most 12- and 16-character alphanumerics) has it
silently decoded into binary garbage. The keyring then fails to open as
"incorrect passphrase" with nothing indicating the file content was
reinterpreted. There is no marker distinguishing encoded from raw.

---

## 31. Medium — `docs/operations.md` is materially stale

> **Deferred by owner ruling, 1 August 2026.** `operations.md` is the
> attribute-level API specification — a tier `spec.md` deliberately does not
> carry — and its crossover with the chain docs (`operations.md`) is reconciled as a separate
> exercise later. The corrections below are inventoried in
> [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
> for that reconciliation to inherit; no plan acts on them now.

The file describes itself as "the authoritative reference for model properties,
operation field validation, defaults" and is wrong about most guardian
economics and several wire facts:

| operations.md claim | Reality |
|---|---|
| `MsgGuardianRevealShare` requires `share_index` (:331) | No such field exists — `tx.proto` has guardian, secret_id, decrypted_share. This is precisely the December 2024 `shareIndex` removal that CLAUDE.md's wire-rename rule was written about, unswept here |
| Slashing is "10,000 VEIL … 5,000 reporter, 1,000 creator, 4,000 burned" (:567, :585-588) | Percentage-of-bond: early reveal 40% burn / 10% creator / 50% reporter (`constants.go:399-402`) |
| Guardians "stake 10,000 VEIL to participate", min 10,000 / max 10,000,000 (:32, :48, :68) | Wire field is `deposit`, may be zero; the fixed cost is the 1,000 VEIL entry fee (`constants.go:212`) |
| "Expired Guardian: stake auto-returned"; withdraw "Returns full stake" (:50, :287) | No auto-return exists; the entry fee is sunk (CHAIN_MECHANICS.md Trade-off §9) |
| `MsgUserRequestGuardians` takes a creator-supplied `reward`, "Minimum 1500 uveil" (:419) | Pricing is protocol-derived (`P = rate × distance × shares × bump`); the creator tunes `bump` |

Separately, the file contains no operations content despite its name — guardian
disaster recovery actually lives in `GUARDIAN_KEY_CUSTODY.md` and
`CONTAINERS.md`. It needs either a full resync against spec.md or a
deprecation banner before testnet; it will be the first result anyone finds for
"timeflare operations".

---

## 32. Medium — CONTAINERS.md ships a healthcheck that cannot work on distroless

`docs/guides/CONTAINERS.md:185` gives
`--health-cmd 'guardiand health --timeout 3'` (and :237 the `timeflared`
equivalent). Docker's `--health-cmd` is always `CMD-SHELL`, which requires
`/bin/sh` — absent from distroless, as **the same document** states at
lines 32-34 ("Healthchecks use exec form … distroless has no shell"). The
compose generator gets it right (`generate-compose.sh:162`).

An operator following the flagship "run a guardian in a container" recipe gets
a permanently `unhealthy` container, and anything keyed on health status
(autoheal, `depends_on`, a Kubernetes translation) misbehaves while the daemon
itself is fine.

Adjacent: `guardian/Dockerfile:47` exposes 21000 and 21100 but omits the
dashboard port 21200 that CONTAINERS.md:13 documents.

---

## 33. Low–Medium — explicit rejection pays an unreimbursed fee where silence is free

Every validation failure broadcasts `MsgGuardianConfirmShares{accept:false}`
(`reveal.go:394-421`). Non-response is a first-class protocol outcome —
spec.md Phase 3: "Candidates may reject, fail the float check, or simply not
respond — progression only requires the band's floor" — and the accept-fee
slices pay only acceptors and revealers (spec.md "Terminal-state disposition"),
so reject gas is never reimbursed. CHAIN_MECHANICS.md Security Observation §2 confirms
non-response is unpriced.

A creator repeatedly distributing garbage to its draws therefore griefs every
selected guardian into a reject fee per secret. Rejection should be treated as
optional courtesy — and certainly not performed on the daemon's own transient
failures (finding 2).

---

## 34. Low–Medium — validation gaps on numeric and duration fields

`Config.Validate()` (`config/config.go:195-291`) checks `polling_interval`,
`block_time`, `request_timeout`, `retry_attempts`, the `max_*` fields and the
ports, but not:

- **`retry_backoff`** — zero or negative accepted; `client.go:97` multiplies
  it, so `time.After(≤0)` fires immediately and retries hammer a struggling
  node with no spacing.
- **`event_reconnect_backoff`** — zero accepted; `events.go:58` becomes a hot
  reconnect loop from every guardian pointed at a down endpoint.
- **`shutdown_timeout`** — zero accepted; `start.go:187` and
  `monitoring/service.go:309` get an already-expired context, so shutdown is
  never graceful.
- **`gas_adjustment`** — zero or negative accepted; `CalculateGas` returns
  `uint64(adjustment × gasUsed)`, so non-reimbursed messages (`register`,
  `update`, `rotate-key`) declare zero gas and are rejected. Reveals and
  confirms survive only because of the `reimbursedGasLimit` floor
  (`signer.go:220-222`).
- **`fee_buffer_percent`** — negative accepted, weakening the register balance
  pre-flight (`registration.go:192`).
- **`gas_price`** — see finding 4.
- **No upper bound on `polling_interval`**: with event monitoring off, a large
  interval can straddle the entire 20–200-block commit window, so assignments
  expire unconfirmed.

`NewActiveSecretCache` (`cache.go:73-78`) is the counter-example done right —
it clamps non-positive values defensively.

---

## 35. Low–Medium — no withdraw verb in `guardiand`

`MsgGuardianWithdrawStake` exists on chain
(`proto/timeflare/secrets/v1/tx.proto:46-47`), and the guardian module contains
no reference to it. The documented exit path — CHAIN_MECHANICS.md Trade-off §9, "a
leaving guardian drains their float" — therefore requires `timeflared`, which
the distroless guardiand image does not contain. A containerised operator who
has served out their window cannot reclaim their float with the tooling they
were told to run.

---

## 36. Low — recovery mnemonic is read with terminal echo

`cmd/guardiand/cmd/key.go:276` reads the 24-word recovery phrase via
`promptForInput`, which is a plain `bufio` read with no echo suppression
(`config.go:113-118`). Every other secret uses `readPasswordInput()`
(`term.ReadPassword`) — including the backup passphrase eighteen lines below at
`key.go:294`.

The words *are* the raw key; `key.go:207-209` says so in its own warning. Typed
with echo they land in terminal scrollback, `script`/`tmux` capture,
screen-share recordings and any terminal-logging agent.

---

## 37. Low — `register --accepting-secrets=false` is silently ignored

`register.go:57`, `:78`, `:114` collect and display the flag, but
`RegistrationOptions` has no such field (`service.go:337-344`) and
`RegisterWithOptions` hardcodes `AcceptingSecrets: true`
(`registration.go:124-130`). The `IsUpdate` branch likewise hardcodes
`accepting := true` (`:107-113`), so that path would re-enable acceptance as a
side effect of a float top-up.

The operator believes they registered paused; the chain says accepting, and the
guardian starts receiving assignments it is not ready for. The standalone
`guardiand update` command is presence-aware and correct — only this path is
broken.

---

## 38. Low — no availability-window lifecycle management

Registration defaults `availableUntil` to `MaxAvailabilityWindow`
(5,256,000 blocks, ~1 year; `registration.go:59-62`) and nothing ever extends
it. Selection requires `available_until ≥ reveal_end_block` (spec.md Guardian
Selection #2, enforced by `x/secrets/keeper/guardian_eligibility.go`), so a
guardian's eligibility for long-dated secrets decays continuously from day one
and reaches zero at expiry — silently.

The daemon exposes only a dashboard `BlocksRemaining`
(`dashboard_source.go:489-490`): no metric, no health signal, no optional
auto-extend. A set-and-forget testnet guardian stops being selected and its
operator is never told. Note this is *not* CHAIN_MECHANICS.md Trade-off §13, which
accepts the shrinking eligible set as a protocol property; the finding is that
the daemon gives the operator no visibility of their own decay.

**Additional defect, found 1 August 2026 while planning the fix**: the
dashboard's `EligibilityWarning` is set to `true` in **both** branches of
`dashboard_source.go:489-502` — `:494` when blocks remain and `:500` when they
do not — so it is unconditionally on for every registered guardian from the
moment of registration. Its note is substantively correct but says the same
thing on day one as on the last day, so the dashboard cannot distinguish a
healthy window from a closing one. The one signal that did exist is therefore
not usable as a signal.

---

## 39. Low — dead code, dead config and dead flags

Each verified by grep across the whole repository; every entry below has its
definition as the only non-test reference.

**Dead config keys** (parsed, documented, defaulted, never read):

| Key | Definition | Consequence |
|---|---|---|
| `log_file_path` | `config.go:111` | `initLogger` (`start.go:206-229`) uses zap's stderr sinks; an operator who configures a log file gets no file, and no log history after a missed reveal |
| `max_concurrent_secrets` | `config.go:80`, validated `> 0` at `:265` | No local concurrency policy exists (finding 16); the only cap is chain-side `MaxActiveBondsPerGuardian` |
| `enable_metrics` | `config.go:96` | `monitoring/service.go:211-236` binds the listener unconditionally; setting false does nothing |
| `enable_health_check` | `config.go:108` | Same — only `EnableDashboard` is honoured (`:246`) |
| `monitor_name` | `config.go:56` | Referenced only by a test fixture |

**Dead functions and methods**:

| Symbol | Location | Note |
|---|---|---|
| `Metrics.SetBalance` | `monitoring/metrics.go:84-90` | See finding 9 — the gauge is registered and never set |
| `Observations.RecordSettlement` | `guardian/observations.go:136-137` | See finding 25 — dashboard panel permanently empty |
| `Service.GetMetrics` | `monitoring/service.go:203-206` | Self-described as "retained as an alias … for existing callers"; there are none |
| `ActiveSecretCache.GetStateCount` | `guardian/cache.go:141-151` | No callers |
| `Config.SetEncryptionPrivateKey` | `config/config.go:413-419` | Doc comment says "used by tests and by flows that generate the key in-process"; only the former is true |
| `Config.HasRetiredEpochKeyFile` | `config/config.go:390-395` | No callers |
| `Health.LastHeight` | `monitoring/health.go:59-60` | No callers; `Snapshot()` reads the atomic directly at `:92` |
| `config.Keys` | `config/registry.go:81` | Only consumer is `config_test.go:87` |
| `ClientInterface.GetSecret` | `blockchain/interface.go:21` | Interface member with no production caller; forces every mock to implement it. (Distinct from the cache's `GetSecretsNeeding*` methods, which are live) |
| `ClientInterface.SignerAddress` | `blockchain/interface.go:16` | Same |

**Exported methods reachable only from tests** — each has its definition as the
sole production occurrence, with all callers in `_test.go`:
`ActiveSecretCache.Get` (`cache.go:90`), `InFlightRegistry.Len`
(`inflight.go:119`), and `HealthStatus.Healthy` (`monitoring/client.go:19` —
the `health` command reads `.Status` directly instead).

**Populated-but-never-read struct fields** — written on every conversion, read
nowhere, and not marshalled to JSON in production:
`blockchain.Secret.Creator` (`types.go:51`, written `:124`),
`RevealedShare.RevealedAtBlock` (`types.go:99`, written `:153`),
`RegistrationStatus.Address` and `.Accepting` (`registration.go:37`, `:39` —
only `.IsRegistered` and `.Stake` are consumed), and `FieldSpec.AltKey`
(`registry.go:21`, populated `:53` — `findFieldSpec` at `:74` re-derives the
kebab mapping with `strings.ReplaceAll` rather than reading it).

**Dead flags and unreachable options**:

- `start --startup-timeout` — registered and defaulted (`start.go:63`),
  documented in three help examples (`:37`, `:49`, `:55`), never read.
  `VerifyRegistration` runs with no deadline.
- `RegistrationOptions.Force` — unreachable; `register` defines no `--force`.
- `status --detailed` — the `detailed` parameter is ignored by `GetStatus`
  (`service.go:352-407`).
- The `IsUpdate` branch of `RegisterWithOptions` (`registration.go:106-113`) —
  no caller passes `IsUpdate: true`; `guardiand update` uses a separate path.
  Its semantics have drifted from the live command (finding 37).

---

## 40. Low — key-material hygiene gaps

All best-effort by nature — the custody guide is honest that a same-host
attacker with daemon privileges wins — but each is cheap to close:

- **Retired keys are never zeroed.** `CollectRetiredKeys` returns raw copies
  (`custody/bundle.go:108`); neither `runKeyBackup` (`key.go:124-128`) nor
  `runRotateKey` zeroes the map, so every retired epoch key stays resident for
  the process lifetime. The *current* key is handled correctly in both
  (`defer custody.Zero(...)`, `key.go:102`, `rotate_key.go:82`).
- **`SaveEncryptedShareKey`'s `key [32]byte` parameter** (`keyfile.go:78`) is a
  by-value copy that is never zeroed.
- **`Bundle.CurrentKeyEpoch` is inferred from disk, not the chain**
  (`key.go:129-134`, `max(retired) + 1`). Deleting a settled epoch's file is
  documented as normal, so a guardian at epoch 5 whose epoch-3/4 files were
  deleted records `CurrentKeyEpoch: 3`. Inert today (nothing reads it on
  restore) but wrong in a persisted artefact, and the same field is
  authoritative at `rotate_key.go:147`.
- **Orphaned doc comment**: the "SetObservability wires the metrics and health
  sinks" sentence sits at `guardian/service.go:119`, directly above
  `SetBuildInfo`, detached from the `SetObservability` it describes at `:130`.
- **Untracked build artefact** `guardian/coverage.html` sits in the module
  root. Not in git, so cosmetic only.
- **`suppressKeyringTTYPrompts` swaps `os.Stdin` process-wide**
  (`blockchain/keys.go:66-75`). The invariant that keeps it safe — every
  interactive prompt happens before the first keyring is constructed — holds
  across all current commands but is enforced only by a comment. A future
  command that opens a keyring before prompting silently gets an empty
  passphrase.

---

## 41. Low — miscellaneous correctness and copy defects

- **Sequence-mismatch recovery cannot fix the pending-own-tx case**
  (`signer.go:169-174`): it refetches the *committed* sequence and retries
  once, which reproduces the same rejection when our own previous transaction
  is still in the mempool. Self-heals next poll; costs one attempt cycle. Also
  `isSequenceMismatch` (`:261-263`) matches by substring under the file's own
  "never by error-string matching" doctrine — `TxResponse.Code` (32) is
  available and sturdier.
- **Whole pagination loop shares one `RequestTimeout`** (`client.go:190-221`,
  `:79`): once the secret store is large enough that paging exceeds 30 s, every
  resync deadline-exceeds and retries from page zero, so the guardian can never
  sync. A growth cliff rather than a degradation.
- **No jitter on backoff**: `withRetry`'s linear backoff (`client.go:97`) and
  the fixed `EventReconnectBackoff` (`events.go:58`) mean every guardian
  re-polls and re-subscribes in lockstep after a node restart.
- **Port-collision check compares ports, not host:port** (`config.go:246-256`):
  rejects `grpc_endpoint: node.example.com:21100` with `metrics_port: 21100`
  though nothing collides — a false positive exactly in the remote-node
  topology a testnet operator uses.
- **Unrepairable-by-CLI config**: any invalid value in the file or a bad
  `GUARDIAN_*` variable makes `initConfig` exit 1 for *every* command
  (`root.go:56-59`), including the `config set` needed to fix it. Repair
  requires hand-editing the YAML.
- **`register` error text names flags that do not exist**
  (`registration.go:88`): "Use --update for updates or --force to re-register".
  Neither is defined on `register`; the real answer is `guardiand update`.
- **Entry-fee copy contradicts itself and the chain**: `register.go:188` says
  the fee is burned, its own help at `:29` says routed to validators, and the
  chain does a 90/10 split (`x/secrets/types/constants.go:208-212`). The same
  stale "burned" claim appears in `tx.proto`'s `MsgGuardianRegister` comment
  and `x/secrets/types/economics.go:223`.
- **`guardian/Makefile:32` tells operators to run `guardiand config show`** —
  no such subcommand exists.
- **Help-text unit footgun**: `cmd/config.go:333-334` shows
  `config set stake-amount 15000uveil` (0.015 VEIL; presumably
  `15000000000uveil` was meant), and `:505` describes "the 10,000 VEIL stake",
  omitting the entry fee.
- **`chain_id` defaults to the devnet's real chain-id** (`config.go:132`,
  `timeflare-test`, matching `devnet/docker/generate-compose.sh:12`). On
  testnet a forgotten `chain-id` yields working queries and universally failing
  transactions — a real-looking wrong value is worse than an empty one
  (see finding 22).
- **`/api/activity` returns verbatim error strings**
  (`dashboard.go:278`, filled at `reveal.go:187`, `:199`, `:369`, `:400`),
  exposing chain rejection logs and the gRPC endpoint on an unauthenticated
  surface. No key material. Not in the dashboard-auth plan's inventory.
- **`/health` payload is not enumerated in the dashboard-auth plan's residual
  list**: it is unauthenticated on `0.0.0.0:21000` *and* mirrored on the
  metrics port (`monitoring/service.go:214-215`, `:227-228`), and
  `key_loadable: false` is a live "this guardian cannot decrypt anything right
  now" beacon.
- **The reveal jitter is publicly derivable**, so publishing
  `planned_reveal_height` and `reveal_offset_blocks` on the dashboard
  (`dashboard.go:119`, `dashboard_source.go:445-447`) only completes what
  `sha256(secret_id + "|" + guardian_address) % (offset+1)` (`reveal.go:286-287`)
  already gives anyone. Worth a ruling on intent rather than a fix: harmless if
  the offset is only mempool de-correlation, a defect if it was meant to deny a
  pre-reveal DoS a fixed target.
- **Trade-off bookkeeping**: the unauthenticated-dashboard ruling lives only in
  code comments and a test (`config.go:99-106`,
  `config/dashboard_config_test.go:20-24`), while CHAIN_MECHANICS.md's own
  convention is that accepted residual costs are recorded there. That test's
  prose also says the dashboard "binds loopback rather than the shared bind
  address" — the opposite of what the code and the test's own assertion do.

---

## 42. Process — two plans marked `done` have undelivered deliverables

Worth separating from the code findings, because the remedy is a process one
rather than a patch. Three of the findings above are not oversights that crept
in: they are items a completed plan explicitly scoped and then did not ship.

- **`DONE_GUARDIAN_IMPROVEMENTS_PLAN.md:54-56`** lists `enable_metrics`,
  `enable_health_check` and `log_file_path` in a table of config keys whose
  defect is "Never read outside the config package", and `:74-75` names all
  three in the phase-1 wire-up scope. The plan is in `done/`; all three are
  still dead (finding 39).
- **`DONE_GUARDIAN_DASHBOARD_PLAN.md:72`** specifies the activity panel be fed
  by "`settlement_stalled` events touching own secrets", and `:37`/`:260` carry
  "observed settlements" through the design. The plan is in `done/`; the entire
  display pipeline ships — `Settlement`, `maxSettlements`,
  `ActivitySettlement`, the JSON mapping, the HTML and the JavaScript — and
  `RecordSettlement` has no production caller, so the panel permanently renders
  its empty state (findings 25 and 39).

Both were reached independently: the dead-code pass found the unused symbols,
and the plans were then checked to see whether the absence was deliberate. It
was not.

The cost is that `done/` currently overstates what is implemented, which is
exactly the reference a pre-testnet readiness review would trust. Whatever
plans come out of this sweep, the completion check for each wants to be "the
deliverable is reachable from production code", not "the code exists" — a
grep for callers would have caught both of these at the time.

---

## Verified sound

Recorded so a future sweep need not re-derive it, and because several of these
are load-bearing.

**Cryptography and custody**

- The envelope format (`custody/envelope.go`) is correct: fresh 16-byte salt
  and 12-byte nonce per seal from `crypto/rand`, the full header bound as AEAD
  additional data, version checked before use, Argon2id at RFC 9106's second
  recommended profile (64 MiB, t=3, p=4). No nonce-reuse path exists, and there
  is **no downgrade path** — no unauthenticated branch, no version-0 fallback,
  and the only alternative format (legacy plaintext) is distinguished by file
  length rather than by anything an attacker controls (`keyfile.go:37-62`).
- Share encryption uses the repo's pinned primitives correctly: ephemeral-static
  X25519, per-message random nonce, ChaCha20-Poly1305, domain-separated
  derivation, byte-pinned by the corpus in `timeflareio/crypto`. Delegating the small-order
  check to `curve25519.X25519` instead of a hand-maintained blacklist is right.
- HMAC comparison is constant-time with a length pre-check
  (`reveal.go:242-247`).
- File permissions are consistently correct: 0600 for key envelopes, passphrase
  files, config and bundles; 0600 files under 0700 directories on restore; 0644
  only for public artefacts, annotated as such.
- **No key material in logs** — every `zap.` call in the module logs secret IDs,
  heights, epoch *numbers*, sizes, transaction hashes and public fingerprints.
- Key generation is encrypted-by-default with no plaintext path; `config
  migrate-key` reloads and byte-compares its own output before declaring
  success.
- Rotation does not invalidate in-flight shares: epochs resolve from the
  secret's creation height against on-chain history, retired keys persist, and
  `verifyEpochKeyring` refuses to start when an in-flight assignment's epoch key
  is missing — closing the gap *before* it becomes a slash. The trial-decrypt
  fallback logs loudly when it disagrees with the derivation rather than
  papering over it.
- No dashboard endpoint exposes share material; the closest is a truncated
  SHA-256 of a *public* key. Every route is GET, nothing mutates, and the
  embedded `fs.Sub` file server is not traversable.

**Protocol conformance**

- All protocol timing uses block heights, never the local clock; `block_time`
  is explicitly display-only. No wall-clock hazards.
- Inclusive window bounds match the keeper on the open side, and the
  reconstructable-still-must-reveal trap is handled (`cache.go:190-199`) with a
  comment explaining what it cost to learn.
- Gas discipline implements CHAIN_MECHANICS.md Trade-off §17 faithfully: declare
  `max(simulated, reimbursed)` so reimbursement equals spend where possible and
  a wide-band accept never aborts; above-floor gas price warned once at startup.
- Restart recovery is chain-derived throughout — cache rebuilt from chain state,
  wrong-share-key refusal, epoch-keyring refusal, in-flight loss costing at most
  one duplicate fee. No local state whose loss causes a protocol violation.
- Reveals are strictly gated on `RevealStartBlock`; the daemon cannot be
  early-slashed by its own operation.
- SIGN_MODE_DIRECT binds chain-id, account number and sequence, so no
  cross-chain or cross-account replay is possible.

**Implementation**

- `go build`, `go vet` and `go test -race ./...` all pass. (The race in finding
  18 is not covered by the suite — the tests never run the poll loop and event
  monitor concurrently.)
- The in-flight registry is correct and well-reasoned: atomic check-and-reserve,
  release on CheckTx failure, height-based expiry, chain-state reconciliation.
- Observation buffers are capped ring buffers with copy-down (no backing-array
  retention) and totals that survive eviction — an unauthenticated dashboard
  cannot grow the daemon's memory.
- Monitoring HTTP lifecycle is sound: synchronous binds fail startup loudly,
  listeners are cleaned up on partial-bind failure, shutdown honours the grace
  period, double-shutdown is safe.
- Keyring plumbing (infinite passphrase replay, TTY suppression via
  `sync.Once`, pipe write-end pinned against finalisers) is careful and correct.
- Reveal parallelism acquires its semaphore before spawning, giving real
  backpressure; tickers are stopped; no per-event goroutine spawning.
- The config registry is tag-derived, so file, env and `config set` share one
  parse path; durations must carry units (a bare `"30"` is rejected), which is
  a genuine unit-confusion defence.
- `config doctor` is the strongest command in the module — effective values,
  cross-field validation, keyring resolution, key-load check and address match,
  exiting non-zero on failure. It should be advertised in place of `config
  list` until finding 10 is fixed.
- The key backup/restore drill is genuinely strong: the bundle carries the whole
  epoch keyring, and restore verifies the derived key against the on-chain
  record before declaring success.
- A restarted or restored guardian cannot double-sign in any slashable sense —
  guardians only reveal, duplicate reveals are chain-rejected, and two daemons
  on one key degrade to sequence-mismatch retries (wasteful, not dangerous).
- CI is solid: the guardian job runs `make -C guardian verify` plus tests with
  `-race`, govulncheck sweeps the module, and a label-gated `e2e-full`
  exercises guardians in the three-validator compose stack.

---

## Plans cut from this sweep

Nine plans, authored 1 August 2026, each carrying its own open questions.
Ordering reflects slashing exposure, not effort. Every finding above has
exactly one owning plan.

| Plan | Priority | Findings owned |
|---|---|---|
| [PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md](PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md) | P0 | 1, 19, 21, 36, 40 |
| [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md) | P0 | 3, 15, 20, 22, 24, part of 41 |
| [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md) | P0 | 2, 12, 13, 14, 16, 17, 18, 33 |
| [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md) | P1 | 4, 5, 10, 11, 29, 30, 34 |
| [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md) | P1 | 6, part of 41 |
| [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md) | P1 | 8, 9, 23, 25, 26, 27, 28 |
| [PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md](PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md) | P1 | 7, part of 41 |
| [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md) | P1 | 32, part of 41 (31 deferred, inventoried there) |
| [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md) | P2 | 35, 37, 38, 39, 42 |

Three sequencing constraints matter, because getting them wrong wastes work:

- **The dead-code sweep must not delete `SetBalance`, `RecordSettlement` or
  `max_concurrent_secrets`.** All three are dead only because the plans that
  consume them (operational visibility, accept/reveal correctness) have not run
  yet.
- **The missed-window counter needs two plans to be trustworthy.** The
  accept/reveal plan stops false `MarkRevealed` calls hiding misses; the
  visibility plan makes detection run on the polling path. Either alone leaves
  the counter under-reporting.
- **Documentation resync should follow the plans that change what it
  documents** — version stamping, health probes, TLS keys and the flags the
  dead-code sweep resolves.

**Owner rulings, 1 August 2026** — folded into the plans, recorded here so the
sweep does not read as still-open:

- **The `SecretsByGuardian` query RPC is approved** (finding 7), sequenced
  behind the measurement in the query plan's phases 1-4 so the query's shape is
  informed by what the remaining cost turns out to be.
- **The stale entry-fee comment in `tx.proto` and `economics.go` is approved
  for correction** (finding 41) — comment-only, riding with the regenerated
  types.
- **`docs/operations.md` stays as it is for now** (finding 31). The sweep's
  recommendation to retire it was wrong: it is the attribute-level API
  specification, a tier spec.md deliberately does not carry, and its crossover
  with the chain docs (operations.md) is reconciled as its own exercise at a later date. Its
  content corrections are inventoried in the documentation plan and are
  **deferred, not dropped** — including the `share_index` entry, which
  documents a wire field that does not exist.

Two fix shapes were also refined while cutting the plans, and the plans are
authoritative where they differ from the fix notes above: finding 12 (the
last-chance reveal attempt is kept rather than suppressed — attempting costs
one fee, not attempting risks the bond) and finding 7 (staged behind
measurement, per the ruling above).

## Method

Six parallel reviews — runtime and concurrency, protocol conformance,
configuration, custody and security, operations, dead code — each required to
produce receipts, followed by independent verification of every claim against
source before it entered this document. Findings that could not be reproduced
were dropped rather than softened.

Tooling: `go build`, `go vet` and `go test -race ./...` (all pass);
`golang.org/x/tools/cmd/deadcode` and `staticcheck -checks U1000` over the
module; an AST extraction of all 758 exported symbols in non-test, non-mock
guardian code with whole-repo reference counts, manually verified for every low
count. Several configuration findings were confirmed empirically by running the
commands (`config list` producing no output, `config set` persisting an
environment override) rather than by reading the code alone.

Two agent claims were rejected on verification and are not recorded above: that
`ClientInterface.GetSecret` has production callers (the apparent hits were the
cache's unrelated `GetSecretsNeeding*` methods), and that the guardian imports
`cosmos-sdk/version` (it does not, which is why finding 28 stands).
