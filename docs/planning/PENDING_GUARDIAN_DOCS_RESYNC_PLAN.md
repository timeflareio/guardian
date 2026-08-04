# Guardian documentation and copy corrections

**Priority**: P1 — the containers guide ships a healthcheck recipe that cannot
work on the image it documents, and the CLI contradicts itself and the chain
about what registration costs.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
finding 32 and the copy defects in finding 41. Finding 31 is inventoried here
but deferred — see "Deferred: the operations.md corrections".
**Components**: `docs/guides/CONTAINERS.md`; `guardian/Dockerfile`;
`guardian/Makefile`; `guardian/cmd/guardiand/cmd/register.go`, `config.go`;
`guardian/guardian/registration.go`;
`guardian/config/dashboard_config_test.go`; `docs/CHAIN_MECHANICS.md`;
`proto/timeflare/secrets/v1/tx.proto`, `x/secrets/types` (regenerated),
`x/secrets/types/economics.go`.

**Protocol surface**: comment-only corrections to `tx.proto` and
`economics.go`. Approved by the owner on 1 August 2026. No wire change, but it
rides with the regenerated types per the protocol-surface rule.

---

## The documentation set, as ruled

Recorded because the sweep initially misread it, and the misreading produced a
recommendation (retire `operations.md`) that was wrong. The three documents
have distinct jobs:

- **`docs/spec.md`** — the protocol authority: a higher-level description of
  each workflow item. It describes *what happens*, and does not necessarily
  break down attributes.
- **`docs/operations.md`** — an API specification: the attribute-level
  reference for each operation's fields, validation and defaults. This is the
  layer spec.md deliberately does not carry.
- **`docs/CHAIN_MECHANICS.md`** — the implementation internals and judgement ledger (the per-operation crossover was consolidated into operations.md, August 2026).

So `operations.md` is not redundant duplication of spec.md; it is the
attribute-level tier, and the drift documented below is a content problem
rather than a structural one. The crossover between all three is real and is
reconciled as its own exercise at a later date (owner ruling, 1 August 2026) —
not from inside a guardian sweep.

---

## Deferred: the operations.md corrections

Inventoried so the future reconciliation inherits them rather than
rediscovering them. **No action in this plan.**

The file describes itself as "the authoritative reference for model properties,
operation field validation, defaults". Verified against the current code and
protocol:

| operations.md claim | Reality |
|---|---|
| `MsgGuardianRevealShare` requires `share_index` (:331) | The field does not exist — `tx.proto` carries guardian, secret_id, decrypted_share. This is the December 2024 `shareIndex` removal that CLAUDE.md's cross-component rule was written about, unswept here |
| Slashing is "10,000 VEIL … 5,000 reporter, 1,000 creator, 4,000 burned" (:567, :585-588) | Percentage-of-bond: early reveal 40% burned, 10% creator, 50% reporter (`x/secrets/types/constants.go:399-402`) |
| Guardians "stake 10,000 VEIL to participate"; min 10,000, max 10,000,000 (:32, :48, :68) | The wire field is `deposit` and may be zero; the fixed cost is the 1,000 VEIL entry fee (`constants.go:212`) |
| "Expired Guardian: stake auto-returned"; withdraw "Returns full stake" (:50, :287) | No auto-return exists and the entry fee is sunk ([CHAIN_MECHANICS.md Trade-off §9](../../CHAIN_MECHANICS.md)) |
| `MsgUserRequestGuardians` takes a creator-supplied `reward`, "Minimum 1500 uveil" (:419) | Pricing is protocol-derived (`P = rate × distance × shares × bump`); the creator tunes `bump` |
| Register/update field named `stake` | The proto field is `deposit` |

These are the numbers a prospective operator sizes their collateral with.

One of them is worth carrying into the reconciliation with a marker on it: the
`share_index` entry is not a stale number but a **documented wire field that
does not exist**. It is the December 2024 removal that CLAUDE.md's
cross-component rule was written about, still unswept here, and it is the kind
of error an integrator builds against before discovering. Everything else on
the list misstates a cost; this one misstates the contract.

Guardian disaster recovery lives in `GUARDIAN_KEY_CUSTODY.md` and
`CONTAINERS.md`. Whether operator procedure belongs under an "operations" name
at all is part of the deferred reconciliation, not a question this plan needs
answered.

---

## The issue

### 1. CONTAINERS.md ships a healthcheck that cannot work

`docs/guides/CONTAINERS.md:185` gives
`--health-cmd 'guardiand health --timeout 3'` (and `:237` the `timeflared`
equivalent). Docker's `--health-cmd` is always `CMD-SHELL`, requiring
`/bin/sh` — absent from distroless, as **the same document** states at
`:32-34`: "Healthchecks use exec form … distroless has no shell". The compose
generator gets it right (`devnet/docker/generate-compose.sh:162`).

An operator following the flagship "run a guardian in a container" recipe gets
a permanently unhealthy container and debugs a daemon that is fine.

Adjacent: `guardian/Dockerfile:47` exposes 21000 and 21100 but omits the
dashboard port 21200 that CONTAINERS.md:13 documents.

### 2. Copy defects that misstate cost or name things that do not exist

- **The entry fee is described three ways.** `register.go:188` says it is
  burned; its own help text at `:29` says routed to validators; the chain does
  a 90/10 split (`constants.go:208-212`). The same stale "burned" claim appears
  in `tx.proto`'s `MsgGuardianRegister` comment and
  `x/secrets/types/economics.go:223`.
- **`registration.go:88`** tells the operator "Use --update for updates or
  --force to re-register". `register` defines neither flag; the answer is the
  separate `guardiand update` command.
- **`guardian/Makefile:32`** instructs `guardiand config show` — no such
  subcommand exists.
- **`cmd/config.go:333-334`** shows `config set stake-amount 15000uveil`, which
  is 0.015 VEIL where `15000000000uveil` was presumably meant; `:505` describes
  "the 10,000 VEIL stake", omitting the entry fee.
- **`config/dashboard_config_test.go:6-7`** says the dashboard "binds loopback
  rather than the shared bind address" — the opposite of what the code and the
  test's own assertion do.
- **CONTAINERS.md:18-19** claims version stamping mirrors the native build;
  true only for `timeflared`.

### 3. An accepted trade-off is recorded only in code

The unauthenticated-dashboard ruling lives in a config comment
(`config/config.go:99-106`) and a test, while CHAIN_MECHANICS.md's own
convention is that accepted residual costs are recorded there.

---

## Design

### Phase 1 — containers guide correctness

Replace the shell-form `--health-cmd` recipes with exec form, matching what the
document already says at `:32-34` and what the compose generator does. Add
21200 to the Dockerfile's `EXPOSE`. Correct the version-stamping claim once
[PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
phase 6 makes it true, or restate it accurately now if this plan lands first.

### Phase 2 — copy sweep

Fix the entry-fee description everywhere it appears in guardian-owned code and
docs, the `registration.go:88` flag names, the Makefile's `config show`, the
`stake-amount` example, and the inverted comment in
`dashboard_config_test.go`. Each is a one-line change; the value is that
together they stop the CLI contradicting itself about what registration costs.

### Phase 3 — correct the entry-fee claim in the wire contract

`tx.proto`'s `MsgGuardianRegister` comment and `x/secrets/types/economics.go:223`
both state the 1,000 VEIL entry fee is burned. It is routed to the fee
collector and rides the next block's 90/10 split — 90% to validator rewards,
10% burned (`x/secrets/types/constants.go:208-212`).

Comment-only, so there is no wire change and no behavioural risk, but it is the
wire contract's own documentation and therefore what an integrator reads. It
lands with the regenerated types as one unit, per the protocol-surface rule.

Worth a grep for the same claim elsewhere while in there — it has already
propagated to three places, which is how it survived this long.

### Phase 4 — record the dashboard trade-off

Add the unauthenticated-dashboard position to CHAIN_MECHANICS.md in the
established form (chose / gave up / where decided), cross-referencing
[DONE_DASHBOARD_AUTHENTICATION_PLAN.md](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)
as the plan that closes it. This is bookkeeping, but the convention exists so
that a reader of CHAIN_MECHANICS.md sees the whole accepted-risk surface in
one place.

---

## What this plan does not solve

- **It does not touch `docs/operations.md`.** Its corrections are inventoried
  above and belong to the chain-docs reconciliation of operations.md and spec.md
  (the operations.md/PROTOCOL.md half was consolidated in August 2026). That exercise should also audit the creator- and
  recipient-facing sections, which carry the same staleness risk against
  different code and were not examined by this sweep.
- **It does not change protocol behaviour.** Every correction aligns
  documentation to code, never the reverse. Where the two disagree, spec.md and
  the constants win by definition.
- **It does not write the operator runbook.** Linking to the existing custody
  and containers guides is in scope; authoring new disaster-recovery procedure
  is not.
- **It does not fix the docs for features other plans change.** The TLS keys,
  the new metrics and the restart-required semantics land with their own plans.

---

## Open questions

1. **Where should the single-endpoint risk be documented?** If
   [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
   defers failover, the residual is an operator-facing risk with no code
   mitigation and, with operations.md deferred, no obvious home.
   *Recommendation: `docs/guides/CONTAINERS.md`, beside the remote-endpoint
   example that invites the topology.* "Run your own node, or accept that its
   downtime is your slashing exposure" is a sentence an operator needs before
   they choose their topology, not after — and the containers guide is where
   that choice is actually made.

2. **Should the deferred operations.md inventory live here, or move to a
   reconciliation plan now?** It currently sits in this plan as a parked
   section.
   *Recommendation: leave it here until the reconciliation plan exists, then
   move it wholesale.* A findings list with no owning plan tends to get lost;
   keeping it attached to a live plan means the next person to open either
   document finds it. It should move rather than be copied, so there is never
   more than one copy to keep true.

---

## Related plans

- [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  — owns the version stamping and the health-probe change that phase 2's
  recipes and claims depend on; if that plan lands first, phase 2 shrinks.
- [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
  — removes `register --accepting-secrets` and other surfaces this plan would
  otherwise document; the two should not describe the same flag differently.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — its restart-required semantics need a documented home here.
- [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  — carries its own CONTAINERS.md changes; they should not conflict with
  phase 2.
