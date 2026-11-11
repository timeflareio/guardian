# Guardian documentation and copy corrections

| | |
|---|---|
| **Priority** | P1 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian, chain |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md); [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md) |
| **Open questions** | 4 (Open questions) |

**Why this priority**: the containers guide ships a healthcheck recipe that cannot
work on the image it documents, and the CLI contradicts itself about what
registration costs.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
finding 32 and the copy defects in finding 41.
**Already landed**: the entry-fee comment in the wire contract (`tx.proto`'s
`MsgGuardianRegister` and `x/secrets/types/economics.go`) states the 90/10 fee
split — `timeflareio/chain` commit 15b795f, 4 August 2026. The `operations.md`
corrections of finding 31 (the `share_index` field, fixed-amount slashing,
the 10,000 VEIL stake, auto-return, the creator-supplied `reward`, `stake`
versus `deposit`) are in `timeflareio/chain` — `docs/operations.md`, from the
chain's August 2026 documentation reconciliation recorded in
`timeflareio/chain` — `DONE_DOCS_ACCURACY_REFRESH_PLAN.md`. The `register` help
text and its `--stake-amount` flag describe the entry fee as routed through the
fee split. `guardiand version` reports the build's real version (commit
7f0b636).
**Components**: in this repository — `Dockerfile`; `Makefile`;
`internal/cli/register.go`, `config.go`; `internal/guardian/registration.go`;
`internal/config/dashboard_config_test.go`. In `timeflareio/chain` —
`docs/guides/CONTAINERS.md`; `docs/CHAIN_MECHANICS.md` (phase 3, if it
proceeds).

---

## The documentation set, as ruled

The chain's three documents have distinct jobs (owner ruling, 1 August 2026),
recorded here because the sweep initially misread them:

- **`docs/spec.md`** — the protocol authority: a higher-level description of
  each workflow item. It describes *what happens*, and does not necessarily
  break down attributes.
- **`docs/operations.md`** — an API specification: the attribute-level
  reference for each operation's fields, validation and defaults.
- **`docs/CHAIN_MECHANICS.md`** — the implementation internals and judgement
  ledger.

All three live in `timeflareio/chain`. Nothing in this plan edits `spec.md` or
`operations.md`.

---

## The issue

### 1. CONTAINERS.md ships a healthcheck that cannot work

`timeflareio/chain` — `docs/guides/CONTAINERS.md:195` gives
`--health-cmd 'guardiand health --timeout 3'` (and `:250` the `timeflared`
equivalent). Docker's `--health-cmd` is always `CMD-SHELL`, requiring
`/bin/sh` — absent from distroless, as **the same document** states at
`:32-34`: "Healthchecks use exec form … distroless has no shell". The compose
generator gets it right (`timeflareio/chain` —
`devnet/docker/generate-compose.sh:169`).

An operator following the flagship "run a guardian in a container" recipe gets
a permanently unhealthy container and debugs a daemon that is fine.

Three further defects in the same guide:

- **`:202`** backs up with `timeflare/guardiand:latest key backup …`. The
  image's entrypoint is `guardiand` (`Dockerfile:64`), and `key` is a
  `guardianctl` verb; the daemon carries only `start`, `health` and `version`.
  The recipe as written fails. The image does ship `guardianctl`
  (`Dockerfile:55`).
- **`:206`** says "a container needs no config file at all if it prefers pure
  env". `guardiand start` refuses to run without a configuration file
  (`requireConfig`, `internal/cli/resolve.go:39`). Which of the two moves is
  open question 3.
- **`:18-19`** claims version stamping mirrors the native build, and describes
  only `timeflared version`. The guardian binaries report the Go module version
  recorded in their build info (`internal/cli/buildinfo.go`), not a
  `git describe`.

Adjacent, in this repository: `Dockerfile:58` exposes 21000 and 21100 but omits
the dashboard port 21200 that CONTAINERS.md documents.

### 2. Copy defects that misstate cost or name things that do not exist

- **The entry fee is described two ways in one command.** The `register`
  confirmation preview says "Burn the 1,000 VEIL entry fee"
  (`internal/cli/register.go:262`); the same command's help text and
  `--stake-amount` flag say it is routed through the chain's fee split
  (`:42-43`, `:72`). The chain does a 90/10 split — 90% to validator rewards,
  10% burned.
- **`internal/guardian/registration.go:88`** tells the operator "Use --update
  for updates or --force to re-register". `register` defines neither flag; the
  answer is the separate `guardianctl update` command.
- **`Makefile:33`** instructs `guardianctl config show` — no such subcommand
  exists.
- **`internal/cli/config.go:108`** shows `config set stake-amount 15000uveil`,
  which is 0.015 VEIL where `15000000000uveil` was presumably meant.
- **`internal/config/dashboard_config_test.go:5-7`** says the dashboard "binds
  loopback rather than the shared bind address" — the opposite of what the code
  and the test's own assertion do.

### 3. The dashboard's access position is recorded in two places, and neither is current

The dashboard's access position is written down in a config comment
(`internal/config/config.go:112-124`) and in `timeflareio/chain` —
`docs/guides/CONTAINERS.md:208-216`. Both state the position of 4 August 2026:
the page carries no credential because it shows only what the chain already
publishes. `timeflareio/chain` — `docs/CHAIN_MECHANICS.md`, whose convention is
that accepted residual costs are recorded there, has no entry for it at all.

The position has since been reversed: the dashboard will carry state-changing
actions and therefore needs a login again (owner, 11 November 2026). That login
is not yet designed; the question lives in
[PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md).
So there is no settled position to record yet — see open question 2.

---

## Design

### Phase 1 — containers guide correctness (chain repository)

Edits to `timeflareio/chain` — `docs/guides/CONTAINERS.md`, landed as a change
in that repository:

- Replace the shell-form `--health-cmd` recipes with exec form, matching what
  the document already says at `:32-34` and what the compose generator does.
- Run the backup step through `guardianctl` rather than the image's default
  entrypoint.
- State how the guardian binaries report their version, separately from
  `timeflared`.
- Resolve the "no config file at all" sentence as open question 3 rules.

In this repository: add 21200 to the `Dockerfile`'s `EXPOSE`.

### Phase 2 — copy sweep (this repository)

Fix the `register` preview's entry-fee line, the `registration.go:88` flag
names, the Makefile's `config show`, the `stake-amount` example, and the
inverted comment in `dashboard_config_test.go`. Each is a one-line change; the
value is that together they stop the CLI contradicting itself about what
registration costs.

Worth a grep for the "burned" entry-fee claim elsewhere while in there — it had
propagated to three places before the wire contract was corrected, which is how
it survived this long.

### Phase 3 — record the dashboard position (chain repository, pending open question 2)

Add the dashboard's access position to `timeflareio/chain` —
`docs/CHAIN_MECHANICS.md` in the established form (chose / gave up / where
decided), and bring the config comment and the CONTAINERS.md paragraph into
line with it. This is bookkeeping, but the convention exists so that a reader
of CHAIN_MECHANICS.md sees the whole accepted-risk surface in one place.

What is recorded depends on the login design in
[PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md),
which does not exist yet.

---

## What this plan does not solve

- **It does not touch `docs/operations.md` or `docs/spec.md`.**
- **It does not change protocol behaviour.** Every correction aligns
  documentation to code, never the reverse. Where the two disagree, spec.md and
  the constants win by definition.
- **It does not write the operator runbook.** Linking to the existing custody
  and containers guides is in scope; authoring new disaster-recovery procedure
  is not.
- **It does not fix the docs for features other plans change.** The TLS keys,
  the new metrics, the withdraw verb and the restart-required semantics land
  with their own plans.
- **It does not design the dashboard login.** That is
  [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md)'s.

---

## Open questions

1. **Where should the single-endpoint risk be documented?** If
   [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
   defers failover, the residual is an operator-facing risk with no code
   mitigation and no obvious home.
   *Recommendation: `timeflareio/chain` — `docs/guides/CONTAINERS.md`, beside
   the remote-endpoint example that invites the topology.* "Run your own node,
   or accept that its downtime is your slashing exposure" is a sentence an
   operator needs before they choose their topology, not after — and the
   containers guide is where that choice is actually made.

2. **What dashboard position does phase 3 record, and when?** The
   unauthenticated position of 4 August 2026 is reversed (owner, 11 November
   2026) and the login that replaces it is not designed.
   *Recommendation: hold phase 3 until
   [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md)
   settles the login, then record that.* Recording the reversed position now
   would write down something already known to be temporary.

3. **Can a guardian container run from environment variables alone?**
   CONTAINERS.md says it can; `guardiand start` refuses without a configuration
   file, by design — a missing file must not exit zero under a supervisor.
   *Recommendation: the guide moves, not the code* — it should say a
   configuration file is required and that every key in it can be overridden
   from the environment. If env-only containers are wanted, that is a behaviour
   change with its own argument.

4. **Who lands the CONTAINERS.md edits?** The guide is chain-owned and four
   guardian plans change it: this one, operational visibility (the readiness
   probe), transport security (the TLS keys) and the dead-code sweep (the
   withdraw verb).
   *Recommendation: this plan lands the correctness fixes of phase 1 as one
   chain change; each other plan adds its own section when its feature ships.*

---

## Related plans

- [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  — owns the health-probe change that phase 1's recipes will then follow; if
  that plan lands first, the recipes are written against `/ready`.
- [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
  — makes `register --accepting-secrets` real and decides the fate of
  `status --detailed` and `start --startup-timeout`; the two should not
  describe the same flag differently.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — its restart-required semantics need a documented home here.
- [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  — carries its own CONTAINERS.md changes; they should not conflict with
  phase 1.
- [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md)
  — where the dashboard login is designed; phase 3 waits on it.
