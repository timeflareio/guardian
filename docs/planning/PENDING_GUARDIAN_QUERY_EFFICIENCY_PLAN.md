# Guardian query efficiency

**Priority**: P1 — load scales as guardians × secrets × transaction rate, and
the resulting node degradation feeds directly into reveal timing. Contains a
growth cliff that stops synchronisation entirely.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
finding 7, plus the pagination-timeout and backoff-jitter items in finding 41.
**Components**: `guardian/blockchain/client.go`; `guardian/guardian/events.go`,
`service.go`, `cache.go`; `guardian/config/config.go`;
`guardian/guardian/cache_test.go`, `integration_test.go`;
`proto/timeflare/secrets/v1/query.proto`, `x/secrets/types` (regenerated),
`x/secrets/keeper/query.go`, `x/secrets/module/autocli.go`, `docs/spec.md`,
`x/secrets/keeper/query_test.go`.

**Protocol surface**: phase 5 adds a query RPC. Approved by the owner on
1 August 2026, sequenced behind the measurement in phases 1-4. It lands as one
unit — spec.md first, then proto, regenerated types, keeper and the AutoCLI
descriptor entry.

---

## The issue

### 1. Every tick and every transaction rescans the whole chain

`blockchain/client.go:187-221` — `ListSecretsForGuardian` paginates the
**global** `Secrets` query at 200 per page and filters client-side. It is
called from two places:

- `UpdateFromBlockchain` on every poll tick (6 s default), and
- `onChainEvent` → `processSecrets` on **any** transaction carrying a
  `secret_*`, `assignment_*` or `guardian_*` event, from **anyone**
  (`events.go:101-123`, `service.go:576-582`).

Each returned `SecretView` is expensive server-side: it carries every
guardian's `encrypted_share` bytes and assignment records
(`proto/timeflare/secrets/v1/query.proto:108-148`), assembled from side stores
by `assembleSecretView`.

Three compounding effects:

- **Load is O(guardians × secrets × transaction-rate).** Fifty guardians and a
  few thousand live secrets means every accept by any guardian causes all fifty
  daemons to re-page the entire secret set, share bytes included.
- **No coalescing.** A block containing twenty secrets transactions delivers
  twenty channel events and therefore twenty sequential full-store scans, on
  top of the poll doing the same.
- **Backpressure inverts into churn.** The callbacks run synchronously in the
  subscription select loop (`events.go:88-108`), so a multi-second rescan backs
  up the 16- and 64-slot channels; CometBFT drops the slow subscriber and the
  monitor reconnects — under exactly the load that caused it.

This is **drift from an approved decision**, not merely an inefficiency.
[DONE_GUARDIAN_IMPROVEMENTS_PLAN.md](../done/DONE_GUARDIAN_IMPROVEMENTS_PLAN.md)
§7.2 accepted client-side filtering, but costed it as "one cheap query per new
secret, network-wide" (:472). The implementation resyncs the full list per
event. The same section logged chain-side enrichment as a later optimisation
requiring explicit approval and a spec update (§9.5).

### 2. The pagination loop shares one timeout — a growth cliff

The entire multi-page loop runs inside a single `withRetry` attempt bounded by
one `RequestTimeout` (`client.go:190-221`, `:79`). Once the secret store is
large enough that paging exceeds 30 s, every resync deadline-exceeds and
retries from page zero — so the guardian can never synchronise again. This is a
cliff rather than a degradation: it works, then it does not, with nothing in
between.

### 3. No jitter on any backoff

`withRetry`'s linear backoff (`client.go:97`) and the fixed
`EventReconnectBackoff` (`events.go:58`) mean every guardian on the network
re-polls and re-subscribes in lockstep after a node restart — a thundering herd
against the node that just came back.

---

## Design

The plan is deliberately staged: the four daemon-side phases land and are
measured first, and the approved protocol change follows, shaped by what that
measurement shows. Phases 1-4 stand on their own — none of them becomes
redundant once phase 5 lands, because coalescing, targeted reads, bounded
pagination and jitter all remain correct against a cheaper query.

### Phase 1 — coalesce event-driven resyncs

Replace the direct `onChainEvent` → `processSecrets` call with a dirty flag
consumed by a single resync worker:

- A qualifying event sets a flag rather than performing work.
- One worker resyncs when the flag is set, at most once per block interval,
  and never concurrently with itself.
- The poll tick clears the flag when it resyncs, so the two paths do not
  duplicate.

This alone collapses the twenty-scans-per-block case to one, removes the
synchronous work from the subscription loop (fixing the drop-and-reconnect
churn), and shrinks the window of the cache race that
[PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
phase 8 closes properly.

### Phase 2 — targeted resync from the event's own secret

Secrets-module transaction events carry `secret_id`. Where the event names a
secret the guardian already tracks, or names one at all, a single `GetSecret`
is enough — `ListSecretsForGuardian` is only needed to *discover* assignments
the daemon does not yet know about.

So the resync becomes two paths: targeted point-reads for named secrets, and a
full list only on the poll tick (and at startup). That is the "one cheap query
per new secret" the approved design specified.

### Phase 3 — bound pagination properly

Give each page its own timeout rather than sharing one across the loop, and
carry the page cursor across retries so a transient failure resumes rather than
restarting. The overall operation gets a generous ceiling so a genuinely stuck
loop still terminates.

### Phase 4 — jitter

Add proportional jitter to `withRetry`'s backoff and to
`EventReconnectBackoff` (for example ±25%). Small change, and it is what stops
a node restart from being followed by a synchronised stampede.

### Phase 5 — chain-side filtering: the `SecretsByGuardian` query

A `SecretsByGuardian` query RPC, served from the per-`(secret, guardian)`
assignment records the keeper already maintains, returning only the requesting
guardian's assignments. That removes the full scan entirely and cuts each
response to the guardian's own share bytes.

This closes the gap
[DONE_GUARDIAN_IMPROVEMENTS_PLAN.md](../done/DONE_GUARDIAN_IMPROVEMENTS_PLAN.md)
§9.5 held open, and it is the reason §7.2's client-side filtering was only ever
"start with (1)".

**Sequenced behind measurement.** Phases 1-4 land and run on the testnet first.
The point is not hesitancy about the RPC but about its shape: the measurement
tells us whether the remaining cost is the poll-tick list (in which case the
query wants pagination over a guardian's assignments) or per-event discovery
(in which case a lighter assignment-only response is the better shape). Adding
the wrong surface is more expensive than adding it a fortnight later, because
a query RPC is permanent once integrators depend on it.

**Landing as one unit**, spec-first per the protocol-surface rule:

1. `docs/spec.md` — the query's semantics and what a guardian may rely on.
2. `proto/timeflare/secrets/v1/query.proto` — the RPC, request and response.
3. `x/secrets/types` — regenerated code (a `types` module release, so the wire
   contract is versioned with the change).
4. `x/secrets/keeper/query.go` — the handler, reading the assignment index
   rather than scanning secrets.
5. `x/secrets/module/autocli.go` — the descriptor entry the CLI/query parity
   rule requires; every query RPC must have a CLI verb.
6. `guardian/blockchain/client.go` — `ListSecretsForGuardian` switches to it.

Two design constraints worth settling during execution rather than after:
the response must be paginated like `Secrets` is, since a guardian at the
100-bond cap plus history is not a bounded set; and it must not become a second
way to read share bytes for *other* guardians, so the handler filters to the
requesting address rather than accepting an arbitrary one as a lookup key.

---

## What this plan does not solve

- **It does not reduce what the chain stores or how it assembles a view.**
  `assembleSecretView`'s cost per secret is unchanged; phases 1-4 reduce how
  often it is paid, and phase 5 reduces how many it is paid for.
- **It does not add caching across restarts.** The cache stays in-memory and
  chain-derived, which is what makes restart recovery trustworthy.
- **It does not change the event subscription's breadth.** The daemon still
  subscribes to all transactions and filters client-side, because assignment
  events carry no guardian address. Phase 5 removes the cost of *answering*
  "does this involve me?", not the need to ask. Adding guardian attributes to
  the assignment events themselves — enabling server-side CometBFT filters — is
  a separate change to the event schema, still unapproved, and deliberately not
  in scope here.
- **It does not fix the reveal-timing consequences directly.** Discovering a
  secret late is what pushes a reveal to the window edge; the edge behaviour
  itself belongs to the accept/reveal plan.

---

## Open questions

1. **Should the full list survive at all once phase 2 lands, or only run at
   startup?** Discovery is its only remaining job, and events should surface
   new assignments.
   *Recommendation: keep it on the poll tick.* The polling loop is the
   safety net for everything the subscription missed, and removing its
   discovery role would make a dropped subscription silently stop finding new
   assignments — the failure mode the fallback exists to prevent. Its frequency
   could reasonably be decoupled from the reveal-timing poll, which is worth
   considering during execution.

2. **What is the right coalescing window?** One block is the natural unit, but
   block time varies by network and `block_time` is documented as display-only.
   *Recommendation: coalesce on a short fixed interval (around one second)
   rather than on `block_time`.* It keeps the daemon's responsiveness
   independent of a config value that is explicitly not authoritative, and at
   any real block time it collapses a block's worth of events into one scan.

3. **Should the per-page timeout be configurable?**
   *Recommendation: no — derive it from `request_timeout` per page.* The
   existing knob already expresses "how long a single chain request may take",
   which is exactly what a page is.

---

## Related plans

- [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
  — consumes the discovery latency this plan reduces; its phase 8 closes the
  cache race whose window phase 1 here narrows.
- [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
  — shares `withRetry` and the client's dial configuration.
- [DONE_GUARDIAN_IMPROVEMENTS_PLAN.md](../done/DONE_GUARDIAN_IMPROVEMENTS_PLAN.md)
  §7.2 and §9.5 — the approved design this plan restores compliance with, and
  the logged option phase 5 would exercise.
