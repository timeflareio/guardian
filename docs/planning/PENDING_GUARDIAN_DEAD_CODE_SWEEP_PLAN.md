# Guardian dead-code sweep

**Priority**: P2 — mostly hygiene, but three entries are operator-visible lies:
a flag that is silently ignored, a config key that promises log retention it
does not provide, and a documented exit path with no command behind it.
**Status**: ready (1 August 2026) — all four open questions ruled by the owner
on 1 August 2026 and folded into the design below.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
findings 35, 37, 38, 39, and the completion-criterion item in finding 42.
**Components**: `guardian/config/config.go`, `registry.go`;
`guardian/cmd/guardiand/cmd/` (`register.go`, `start.go`, `status.go`,
`config.go`, plus a new `withdraw.go`); `guardian/guardian/` (`service.go`,
`registration.go`, `cache.go`, `observations.go`, `inflight.go`,
`dashboard_source.go`); `guardian/blockchain/` (`interface.go`, `client.go`,
`types.go`); `guardian/monitoring/` (`service.go`, `metrics.go`, `health.go`,
`client.go`); `guardian/dashboard/` (`dashboard.go`, `assets/app.js`);
`guardian/guardian/mocks/`; `docs/guides/CONTAINERS.md` (withdraw in the
operator lifecycle); `docs/planning/README.md` (completion criterion).

---

## The issue

Everything below was verified by whole-repository grep, `deadcode` and
`staticcheck -checks U1000`, with each entry's definition confirmed as its only
production reference. The list is grouped by what removing or wiring it costs.

### 1. Operator-visible: things that claim to work and do not

- **`register --accepting-secrets=false` is ignored.** The flag is collected
  (`register.go:57`, `:78`), shown in the confirmation preview (`:114`,
  `:179-182`) and referenced in the success text (`:226-232`), but
  `RegistrationOptions` has no such field (`service.go:337-344`) and
  `RegisterWithOptions` hardcodes `AcceptingSecrets: true`
  (`registration.go:124-130`). An operator who registers paused is registered
  accepting, and starts receiving assignments they are not ready for.
- **`log_file_path` does nothing.** Defined and documented as "Log file path
  (empty = stderr)" (`config.go:111`), never read; `initLogger` takes only
  level and format (`start.go:206-229`). An operator who configures a log file
  has no log history after a missed reveal — the forensics gap that matters
  most.
- **No withdraw verb exists.** `MsgGuardianWithdrawStake` is on the chain
  (`proto/timeflare/secrets/v1/tx.proto:46-47`); the guardian module never
  references it. The documented exit path —
  [CHAIN_MECHANICS.md Trade-off §9](../../CHAIN_MECHANICS.md), "a leaving guardian
  drains their float" — therefore requires `timeflared`, which the distroless
  guardian image does not contain.
- **`status --detailed` is a no-op.** The flag is read (`status.go:55`) and
  passed to `GetStatus(ctx, detailed)`, whose body never references it
  (`service.go:352-407`).
- **`start --startup-timeout` is never read.** Registered and defaulted
  (`start.go:63`), advertised in three help examples (`:37`, `:49`, `:55`); the
  pre-flight runs with no deadline.

### 2. Dead configuration keys

| Key | Definition | Note |
|---|---|---|
| `max_concurrent_secrets` | `config.go:80`, default 100, validated `> 0` at `:265` | No consumer; the only cap is the chain's `MaxActiveBondsPerGuardian` |
| `enable_metrics` | `config.go:96` | `monitoring/service.go:211-236` binds the listener unconditionally |
| `enable_health_check` | `config.go:108` | Same; only `EnableDashboard` is honoured (`:246`) |
| `monitor_name` | `config.go:56` | Set only by a test fixture |
| `log_file_path` | `config.go:111` | See above |

### 3. Dead functions, methods and fields

| Symbol | Location |
|---|---|
| `Metrics.SetBalance` | `monitoring/metrics.go:84-90` |
| `Observations.RecordSettlement` | `guardian/observations.go:136-137` |
| `Service.GetMetrics` | `monitoring/service.go:203-206` — comment claims "existing callers"; there are none |
| `ActiveSecretCache.GetStateCount` | `guardian/cache.go:141-151` |
| `Config.SetEncryptionPrivateKey` | `config/config.go:413-419` — comment claims in-process key-generation flows use it; none do |
| `Config.HasRetiredEpochKeyFile` | `config/config.go:390-395` |
| `Health.LastHeight` | `monitoring/health.go:59-60` — `Snapshot()` reads the atomic directly |
| `config.Keys` | `config/registry.go:81` — only `config_test.go:87` |
| `ClientInterface.GetSecret` | `blockchain/interface.go:21` — no production caller; every mock must implement it |
| `ClientInterface.SignerAddress` | `blockchain/interface.go:16` |

Reachable only from tests: `ActiveSecretCache.Get` (`cache.go:90`),
`InFlightRegistry.Len` (`inflight.go:119`), `HealthStatus.Healthy`
(`monitoring/client.go:19` — the `health` command reads `.Status` directly).

Populated but never read: `blockchain.Secret.Creator` (`types.go:51`, written
`:124`), `RevealedShare.RevealedAtBlock` (`types.go:99`, written `:153`),
`RegistrationStatus.Address` and `.Accepting` (`registration.go:37`, `:39`),
`FieldSpec.AltKey` (`registry.go:21`, populated `:53` — `findFieldSpec` at
`:74` re-derives the mapping instead).

### 4. An unreachable branch with drifted semantics

`RegistrationOptions.IsUpdate` and `.Force` are never set by the sole caller
(`register.go:131`). Consequently the `if opts.IsUpdate { GuardianUpdate(...) }`
branch (`registration.go:106-113`) is unreachable, `!opts.Force` (`:79`) is
always true, and the error at `:88` names flags that do not exist.

It is worse than dead: it is a *second implementation* of the update path — the
live one is `cmd/update.go:119` calling `client.GuardianUpdate` directly — and
the two have drifted. The dead branch hardcodes `accepting := true`, so were it
ever reached it would silently re-enable acceptance as a side effect of a float
top-up. Two implementations of one concern is the defect the architectural
minimalism rule names.

### 5. The availability warning is permanently on

`dashboard_source.go:489-502` computes `BlocksRemaining` and then sets
`EligibilityWarning = true` in **both** branches — `:494` when blocks remain
and `:500` when they do not. So for any registered guardian with a future
`available_until`, the warning is unconditionally on from the moment of
registration.

The note it carries is accurate in substance (selection requires
`available_until ≥ reveal_end_block`, so a shrinking window silently excludes
long-dated secrets before it expires) but it says the same thing on day one as
it does on the last day. A warning that is always on is a warning an operator
learns to ignore, and it leaves the dashboard unable to distinguish "healthy"
from "closing" — which is precisely the question it exists to answer.

`BlocksRemaining` is rendered (`dashboard/assets/app.js:326-327`) but buried in
the config panel as a bare number, and no metric carries it at all.

### 6. Trivia

An orphaned doc comment ("SetObservability wires the metrics and health
sinks…") sits at `guardian/service.go:119` above `SetBuildInfo`, detached from
the `SetObservability` it describes at `:130`. An untracked `coverage.html`
sits in the module root.

---

## Design

The sweep is not uniformly "delete". Each entry resolves one of three ways, and
the split is what the plan is for.

### Phase 1 — wire what other plans are about to need

Three symbols are dead *because their consumers were never built*, and are
being brought to life elsewhere. This plan must not delete them:

- `Metrics.SetBalance` and the balance gauge —
  [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  phase 2.
- `Observations.RecordSettlement` — that plan's phase 4.
- `max_concurrent_secrets` —
  [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
  phase 6, pending its open question 3.

Sequencing matters: if this plan runs first it must leave these three alone; if
it runs last they will no longer be dead. Either order works provided the
dependency is respected.

### Phase 2 — fix the operator-visible lies

Each needs a decision rather than a deletion:

- **`register --accepting-secrets`**: add the field to `RegistrationOptions`
  and pass it through, so the flag does what it says. Removing the flag instead
  would be worse — registering paused is a legitimate and useful thing to do.
- **`log_file_path`**: wire it into `initLogger` via a zap file sink. Deleting
  it would leave the daemon with no log-retention story at all, which the
  missed-reveal forensics case needs.
- **`status --detailed`**: give it content — the pending-reveal and
  window-proximity view that
  [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  open question 4 proposes — or remove the flag. Not both.
- **`start --startup-timeout`**: apply it to the pre-flight, or remove it.
- **`guardiand withdraw`**: add it. A thin wrapper over the existing
  `MsgGuardianWithdrawStake`, completing a lifecycle the docs already describe
  and that a containerised operator currently cannot perform at all, since the
  distroless image ships no `timeflared`.

  It must refuse while any bond is locked, naming the outstanding assignments
  and their reveal windows rather than returning a bare chain error — the
  failure mode to design against is an operator draining their float and then
  discovering they still owe reveals they can no longer cover. Partial
  withdrawal down to the locked total is the useful behaviour; withdrawing
  everything is the special case of having no bonds.

  This does not change [CHAIN_MECHANICS.md Trade-off §9](../../CHAIN_MECHANICS.md):
  registration stays permanent and the entry fee stays sunk. The command drains
  the float, which is exactly what §9 already describes as the exit.

### Phase 3 — delete what is genuinely dead

The symbols in issue 3 that no plan claims, the unreachable `IsUpdate`/`Force`
branch and its misleading error text, `monitor_name`, the orphaned comment, and
`coverage.html` (plus a `.gitignore` entry).

**`enable_metrics` and `enable_health_check` are honoured rather than removed.**
Both gate a listener that binds on `bind_address`, defaulting to `0.0.0.0`, so
an operator on an exposed host has a real reason to close either port, and the
fields already carry sensible names and defaults.

The residual risk is worth naming because it is the argument that nearly went
the other way: `/health` is what supervisors probe, so a disabled health
listener is a way to silently break your own monitoring — and
[PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
phase 1 is simultaneously making that probe load-bearing. Mitigation: both
default to true, and disabling the health listener logs a warning at startup
naming what stops working (`guardiand health`, the compose healthcheck, any
restart-on-unhealthy policy). Honouring a flag that turns off monitoring is
defensible; doing it silently is not.

**Struct fields**: keep `blockchain.Secret.Creator` and
`RevealedShare.RevealedAtBlock` — they mirror proto fields, and a conversion
struct that silently drops fields is a trap for the next person who needs one.
Remove `RegistrationStatus.Address` and `.Accepting` and `FieldSpec.AltKey`,
which have no such excuse.

One item needs care rather than a straight delete: removing
`ClientInterface.GetSecret` and `.SignerAddress` shrinks an interface that
mocks implement, so it is a cross-component change within the module — mocks
and any test doubles must be swept with it.

### Phase 4 — availability-window visibility

Registration defaults `availableUntil` to `MaxAvailabilityWindow` (5,256,000
blocks, ~1 year; `registration.go:59-62`) and nothing ever extends it.
Selection requires `available_until ≥ reveal_end_block`, so eligibility decays
from day one and reaches zero silently.

**Windows do not auto-extend.** Extending is a capital commitment — it keeps
the operator liable for reveal obligations further into the future — and taking
that decision on their behalf is the wrong default. The daemon's job is to make
the decay visible in time to act on.

The decay has no cliff, which shapes the whole design. A guardian with `N`
blocks remaining is already excluded from every secret revealing beyond `N`, so
what shrinks continuously is the *addressable set of distances*, not a
capability that switches off on a date. The honest presentation is therefore a
continuous number, not only a threshold alarm.

**Metric.** A `guardian_availability_blocks_remaining` gauge, alongside
`guardian_available_until_height`. Continuous, so an operator can graph the
decay, alert at whatever threshold suits the distances they intend to serve,
and see the step change when they extend. This is deliberately distinct from
`guardian_blocks_to_nearest_reveal_deadline`
([PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
phase 2), which measures an obligation already accepted; this one measures
future eligibility. Both are "how close is a window", and conflating them would
hide whichever is less urgent.

**Dashboard.** `BlocksRemaining` already exists but is a bare number in the
config panel (`dashboard/assets/app.js:326-327`). It moves to the vitals
surface with its horizon expressed in time as well as blocks (blocks are the
protocol's unit; days are the operator's), and — the substantive fix — the
always-on `EligibilityWarning` becomes genuinely conditional:

- **Healthy**: remaining is comfortable; show the number, no warning.
- **Narrowing**: remaining has fallen below `availability_warning_blocks`; warn
  with the note that already exists, which correctly explains the
  `available_until ≥ reveal_end_block` exclusion.
- **Expired**: remaining ≤ 0; the existing "not a selection candidate for
  anything" message.

**Health signal.** The narrowing state raises a health signal so an operator
who watches `/health` rather than Grafana still learns about it.

**The threshold.** A new `availability_warning_blocks` config key, defaulting
to 100,000 blocks (~7 days at 6 s). It has to be configurable because the
meaningful value depends on the distances an operator wants to serve — someone
serving only short secrets cares far later than someone serving year-long ones
— and no single constant can be right for both. Validated positive and below
`MaxAvailabilityWindow`.

**`guardiand status`** shows the same figure, so the answer does not require a
browser. This is also part of what gives `status --detailed` real content
(phase 2).

### Phase 5 — the completion criterion

Finding 42 established that two plans in `done/` have deliverables that were
never wired: `DONE_GUARDIAN_IMPROVEMENTS_PLAN.md:54-56` scoped exactly the
`enable_metrics`, `enable_health_check` and `log_file_path` repairs above, and
`DONE_GUARDIAN_DASHBOARD_PLAN.md:72` specified the settlement feed that
`RecordSettlement` still waits for.

Both would have been caught by asking "is the deliverable reachable from
production code?" rather than "does the code exist". That question belongs in
`docs/planning/README.md`'s execution rules, so it applies to every plan rather
than being re-learned here.

---

## What this plan does not solve

- **It is no longer purely a deletion sweep, and the title undersells it.**
  Two rulings gave it genuine scope: `guardiand withdraw` is a new command
  (phase 2), and phase 4 adds a metric, a config key and a dashboard change.
  Both are completions of lifecycles the project already documents rather than
  new capability, but neither is hygiene, and they should be reviewed on their
  own terms rather than waved through as cleanup.
- **It does not extend availability windows, manually or automatically.**
  Phase 4 makes the decay visible; acting on it stays `guardiand update`.
- **It does not remove the `TxSubmitter` seam** (`blockchain/interface.go`),
  which is deliberately unused pending a KMS backend that remains descoped.
  Unused-by-design is not dead.
- **It does not touch the mocks package.** It shows as unreachable by
  construction and is out of scope except where phase 3's interface shrink
  forces a sweep.
- **It does not resolve `max_concurrent_secrets`.** That decision belongs to
  the accept/reveal plan, which is where the policy would live.

---

## Open questions

None outstanding. All four were ruled on 1 August 2026 and are folded into the
design above: add `guardiand withdraw` (phase 2), honour both listener toggles
with a warning when health is disabled (phase 3), warn rather than auto-extend
availability windows *and* make the proximity continuously visible in the
dashboard and metrics (phase 4), and keep the wire-mirror struct fields while
removing the rest (phase 3).

Two decisions were made inside the design rather than raised as questions,
noted here so they are visible rather than buried:

- The availability warning threshold is a new `availability_warning_blocks`
  config key rather than a constant, because the meaningful value depends on
  the distances the operator intends to serve. Default 100,000 blocks (~7 days
  at 6 s).
- Phase 4 grew a defect the sweep had not caught: `EligibilityWarning` is
  currently set in both branches of `dashboard_source.go:489-502` and is
  therefore always on. Fixing it is what makes the rest of phase 4 legible.

---

## Related plans

- [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
  — brings `SetBalance` and `RecordSettlement` to life; phase 1 here exists to
  avoid deleting them out from under it.
- [PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md](PRIORITY_GUARDIAN_ACCEPT_REVEAL_CORRECTNESS_PLAN.md)
  — owns the `max_concurrent_secrets` decision.
- [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  — deliberately leaves these keys alone; the two plans partition the config
  surface between validation and wire-or-delete.
- [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
  — must not document a flag this plan removes, or omit one it makes real.
