# Guardian dead-code sweep

| | |
|---|---|
| **Priority** | P2 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian, chain |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md); [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md) |
| **Open questions** | 8 (Open questions) |

**Why this priority**: mostly hygiene, but three entries are operator-visible lies:
a flag that is silently ignored, a config key that promises log retention it
does not provide, and a documented exit path with no command behind it.
**Status notes**: four questions were ruled by the owner on 1 August 2026 and
are folded into the design below. Eight more, raised by the plan audit of
11 November 2026, are open — the design is written against a command layout
and a daemon the code has since moved away from.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
findings 35, 37, 38, 39, and the completion-criterion item in finding 42.
**Already landed**: `coverage.html` is ignored (`.gitignore`) and untracked, so
nothing of it remains for this plan.
**Components**: `internal/config/config.go`, `registry.go`;
`internal/cli/` (`register.go`, `start.go`, `status.go`, plus `withdraw.go`
(new)); `internal/guardian/` (`service.go`, `registration.go`, `cache.go`,
`observations.go`, `inflight.go`, `dashboard_source.go`); `internal/chain/`
(`interface.go`, `client.go`, `types.go`); `internal/monitoring/`
(`service.go`, `metrics.go`, `health.go`, `client.go`); `internal/dashboard/`
(`dashboard.go`, `assets/app.js`); `internal/guardian/mocks/`;
`docs/planning/README.md` (completion criterion); and in `timeflareio/chain`,
`docs/guides/CONTAINERS.md` (withdraw in the operator lifecycle).

---

## The issue

Everything below was verified by whole-repository grep, `deadcode` and
`staticcheck -checks U1000`, with each entry's definition confirmed as its only
production reference, and re-checked by grep on 11 November 2026. The list is
grouped by what removing or wiring it costs. File paths in this section are
under `internal/` unless they say otherwise.

### 1. Operator-visible: things that claim to work and do not

- **`register --accepting-secrets=false` is ignored.** The flag is declared and
  read (`cli/register.go:72`, `:93`), shown in the confirmation preview
  (`:253`) and referenced in the success text (`:299-305`), but
  `RegistrationOptions` has no such field (`guardian/service.go:337-344`) and
  `RegisterWithOptions` hardcodes `AcceptingSecrets: true`
  (`guardian/registration.go:124-130`). An operator who registers paused is
  registered accepting, and starts receiving assignments they are not ready
  for.
- **`log_file_path` does nothing.** Defined and documented as "Log file path
  (empty = stderr)" (`config/config.go:130`), never read; `initLogger` takes
  only level and format (`cli/start.go:212`). An operator who configures a log
  file has no log history after a missed reveal — the forensics gap that
  matters most.
- **No withdraw verb exists.** `MsgGuardianWithdrawStake` is on the chain
  (`timeflareio/chain` — `proto/timeflare/secrets/v1/tx.proto:46-47`); this
  repository never references it. The documented exit path —
  `timeflareio/chain` — `docs/CHAIN_MECHANICS.md` Trade-off §9, "a leaving
  guardian drains their float" — therefore requires `timeflared`, which the
  distroless guardian image does not contain. The image ships `guardiand` and
  `guardianctl` (`Dockerfile:54-55`).
- **`status --detailed` is a no-op.** The flag is read (`cli/status.go:58`) and
  passed to `GetStatus(ctx, detailed)`, whose body never references it
  (`guardian/service.go:352`).
- **`start --startup-timeout` is never read.** Registered and defaulted
  (`cli/start.go:55`); the pre-flight runs with no deadline.

### 2. Dead configuration keys

| Key | Definition | Note |
|---|---|---|
| `max_concurrent_secrets` | `config/config.go:97`, default 100, validated `> 0` at `:312` | No consumer; the only cap is the chain's `MaxActiveBondsPerGuardian` |
| `enable_metrics` | `config/config.go:111` | `monitoring/service.go`'s `Start` binds the listener unconditionally |
| `enable_health_check` | `config/config.go:127` | Same; only `EnableDashboard` is honoured (`monitoring/service.go:247`) |
| `monitor_name` | `config/config.go:74` | Set only by its default |
| `log_file_path` | `config/config.go:130` | See above |

### 3. Dead functions, methods and fields

| Symbol | Location |
|---|---|
| `Metrics.SetBalance` | `monitoring/metrics.go:87` |
| `Observations.RecordSettlement` | `guardian/observations.go:137` |
| `Service.GetMetrics` | `monitoring/service.go:204` — comment claims "existing callers"; there are none |
| `ActiveSecretCache.GetStateCount` | `guardian/cache.go:142` |
| `Config.SetEncryptionPrivateKey` | `config/config.go:453` — comment claims in-process key-generation flows use it; none do |
| `Config.HasRetiredEpochKeyFile` | `config/config.go:430` |
| `Health.LastHeight` | `monitoring/health.go:60` — `Snapshot()` reads the atomic directly |
| `config.Keys` | `config/registry.go:85` — only `config_test.go:87` |
| `ClientInterface.GetSecret` | `chain/interface.go:21` — no production caller; every mock must implement it (see open question 3) |
| `ClientInterface.SignerAddress` | `chain/interface.go:16` |

Reachable only from tests: `ActiveSecretCache.Get` (`guardian/cache.go:90`),
`InFlightRegistry.Len` (`guardian/inflight.go:119`), `HealthStatus.Healthy`
(`monitoring/client.go:19` — the `health` command reads `.Status` directly).

Populated but never read: `chain.Secret.Creator` (`chain/types.go:51`, written
`:124`), `RevealedShare.RevealedAtBlock` (`chain/types.go:99`, written `:153`),
`RegistrationStatus.Address` and `.Accepting` (`guardian/registration.go:37`,
`:39`). `FieldSpec.AltKey` (`config/registry.go:21`, populated `:56`) was on
this list when the sweep was run; `LookupField` now reads it (`:114`) — see
open question 4.

### 4. An unreachable branch with drifted semantics

`RegistrationOptions.IsUpdate` and `.Force` are never set by the sole caller
(`cli/register.go:168`). Consequently the
`if opts.IsUpdate { GuardianUpdate(...) }` branch
(`guardian/registration.go:106-113`) is unreachable, `!opts.Force` (`:79`) is
always true, and the error at `:88` names flags that do not exist.

It is worse than dead: it is a *second implementation* of the update path — the
live one is `cli/update.go:118` calling `client.GuardianUpdate` directly — and
the two have drifted. The dead branch hardcodes `accepting := true`, so were it
ever reached it would silently re-enable acceptance as a side effect of a float
top-up. Two implementations of one concern is the defect the architectural
minimalism rule names.

### 5. The availability warning is permanently on

`guardian/dashboard_source.go:463-475` computes `BlocksRemaining` and then sets
`EligibilityWarning = true` in **both** branches — `:468` when blocks remain
and `:473` when they do not. So for any registered guardian with a future
`available_until`, the warning is unconditionally on from the moment of
registration.

The note it carries is accurate in substance (selection requires
`available_until ≥ reveal_end_block`, so a shrinking window silently excludes
long-dated secrets before it expires) but it says the same thing on day one as
it does on the last day. A warning that is always on is a warning an operator
learns to ignore, and it leaves the dashboard unable to distinguish "healthy"
from "closing" — which is precisely the question it exists to answer.

`BlocksRemaining` is rendered (`dashboard/assets/app.js:322`) but buried in
the config panel as a bare number, and no metric carries it at all.

### 6. Trivia

An orphaned doc comment ("SetObservability wires the metrics and health
sinks…") sits at `guardian/service.go:119` above `SetBuildInfo`, detached from
the `SetObservability` it describes at `:130`.

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
  open question 4 proposes — or remove the flag. Not both. See open question 5.
- **`start --startup-timeout`**: apply it to the pre-flight, or remove it. See
  open question 6.
- **`guardiand withdraw`**: add it. A thin wrapper over the existing
  `MsgGuardianWithdrawStake`, completing a lifecycle the docs already describe
  and that a containerised operator currently cannot perform at all, since the
  distroless image ships no `timeflared`. Which binary carries it is open
  question 1.

  It must refuse while any bond is locked, naming the outstanding assignments
  and their reveal windows rather than returning a bare chain error — the
  failure mode to design against is an operator draining their float and then
  discovering they still owe reveals they can no longer cover. Partial
  withdrawal down to the locked total is the useful behaviour; withdrawing
  everything is the special case of having no bonds.

  This does not change `timeflareio/chain` — `docs/CHAIN_MECHANICS.md`
  Trade-off §9: registration stays permanent and the entry fee stays sunk. The
  command drains the float, which is exactly what §9 already describes as the
  exit.

  The operator lifecycle in `timeflareio/chain` — `docs/guides/CONTAINERS.md`
  gains the verb. That is an edit in the chain repository, landing after the
  guardian release that carries the command.

### Phase 3 — delete what is genuinely dead

The symbols in issue 3 that no plan claims, the unreachable `IsUpdate`/`Force`
branch and its misleading error text, `monitor_name`, and the orphaned comment.

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
naming what stops working: `guardiand health`, the compose healthcheck, any
restart-on-unhealthy policy, and `guardianctl register`'s check that the
service is running before it registers (`internal/cli/register.go`,
`confirmServiceRunning`), which probes the same endpoint. Honouring a flag that
turns off monitoring is defensible; doing it silently is not.

**Struct fields**: keep `chain.Secret.Creator` and
`RevealedShare.RevealedAtBlock` — they mirror proto fields, and a conversion
struct that silently drops fields is a trap for the next person who needs one.
Remove `RegistrationStatus.Address` and `.Accepting` and `FieldSpec.AltKey`,
which have no such excuse (for `AltKey`, see open question 4).

One item needs care rather than a straight delete: removing
`ClientInterface.GetSecret` and `.SignerAddress` shrinks an interface that
mocks implement, so it is a cross-component change within the module — mocks
and any test doubles must be swept with it. Whether `GetSecret` goes at all is
open question 3.

### Phase 4 — availability-window visibility

Registration defaults `availableUntil` to `MaxAvailabilityWindow` (5,256,000
blocks, ~1 year; `internal/guardian/registration.go:59-62`) and nothing ever
extends it. Selection requires `available_until ≥ reveal_end_block`, so
eligibility decays from day one and reaches zero silently.

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
config panel (`internal/dashboard/assets/app.js:322`). It moves to the vitals
surface with its horizon expressed in time as well as blocks (blocks are the
protocol's unit; days are the operator's — see open question 2), and — the
substantive fix — the always-on `EligibilityWarning` becomes genuinely
conditional:

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

Finding 42 established that two completed plans have deliverables that were
never wired: `timeflareio/chain` — `DONE_GUARDIAN_IMPROVEMENTS_PLAN.md` scoped
exactly the `enable_metrics`, `enable_health_check` and `log_file_path` repairs
above, and `timeflareio/chain` — `DONE_GUARDIAN_DASHBOARD_PLAN.md` specified the
settlement feed that `RecordSettlement` still waits for.

Both would have been caught by asking "is the deliverable reachable from
production code?" rather than "does the code exist". That question belongs in
`docs/planning/README.md`'s execution rules, so it applies to every plan rather
than being re-learned here. That README states its rules are the same ones
every repository follows; where the criterion is written is open question 8.

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
- **It does not remove the `TxSubmitter` seam** (`internal/chain/interface.go`),
  which is deliberately unused pending a KMS backend that remains descoped.
  Unused-by-design is not dead.
- **It does not touch the mocks package.** It shows as unreachable by
  construction and is out of scope except where phase 3's interface shrink
  forces a sweep.
- **It does not resolve `max_concurrent_secrets`.** That decision belongs to
  the accept/reveal plan, which is where the policy would live.

---

## Open questions

1. **Which binary carries `withdraw`?** The design adds `guardiand withdraw`,
   but the daemon carries only `start`, `health` and `version`; every operator
   verb is in `guardianctl`, which the image also ships.
   *Recommendation: `guardianctl withdraw`.* The same answer applies to the
   plan's `guardiand status` and `guardiand update` references.
   [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md)
   §2 claims the same verb; one plan should own it.
2. **How is the availability horizon expressed (phase 4)?** The design shows it
   in time as well as blocks, which needs a block cadence. The daemon carries
   none and does not convert (owner, 6 August 2026).
   *Recommendation: blocks only*, including the `availability_warning_blocks`
   default, stated without a duration.
3. **Does `ClientInterface.GetSecret` go?** Phase 3 deletes it;
   [PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md](PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md)
   phase 2 is built on single `GetSecret` point-reads.
   *Recommendation: keep it*, and add it to phase 1's do-not-delete list.
4. **Does `FieldSpec.AltKey` go?** Phase 3 lists it as dead. It supplies the
   flag names the config flag layer binds.
   *Recommendation: keep it* — drop it from the deletion list.
5. **`status --detailed`: content, or removal?** The design says one or the
   other and picks neither.
   *Recommendation: remove the flag here.*
   [PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md](PENDING_GUARDIAN_OPERATIONAL_VISIBILITY_PLAN.md)
   open question 4 decides whether a detailed view exists, and adds it with
   content if it does.
6. **`start --startup-timeout`: apply it, or remove it?** Also left as either.
   *Recommendation: remove it.* The flag is registered and never read.
7. **Does the sweep take in dead code it does not list?** Two entries have no
   production use and are in none of the tables above:
   `Service.RegisterWithOptions` (`internal/guardian/service.go:347`), which
   has no caller now that `register` drives the registration manager directly;
   and `start`'s hidden `--accept` flag (`internal/cli/start.go:62`), accepted
   and ignored so that recipes still passing it do not fail —
   `timeflareio/chain` — `docs/guides/CONTAINERS.md` is one.
   *Recommendation: add `Service.RegisterWithOptions` to phase 3; leave
   `--accept` until the chain's recipes have dropped it, then remove it.*
8. **Where does the completion criterion go (phase 5)?** The planning README
   says its rules are shared by every repository, and each repository keeps its
   own copy.
   *Recommendation: write it into this repository's README here, and raise the
   same sentence for the others as their own small changes*, so the rule does
   not exist in one copy only.

Ruled on 1 August 2026 and folded into the design above: add `guardiand withdraw` (phase 2), honour both listener toggles
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
  currently set in both branches of
  `internal/guardian/dashboard_source.go:463-475` and is therefore always on.
  Fixing it is what makes the rest of phase 4 legible.

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
- [PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md](PENDING_GUARDIAN_QUERY_EFFICIENCY_PLAN.md)
  — its phase 2 reads through `ClientInterface.GetSecret`, which phase 3 here
  deletes (open question 3).
- [PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md](PENDING_DASHBOARD_GUARDIAN_ACTIONS_PLAN.md)
  — its §2 adds the same withdraw verb and client method (open question 1).
- [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
  — must not document a flag this plan removes, or omit one it makes real.
