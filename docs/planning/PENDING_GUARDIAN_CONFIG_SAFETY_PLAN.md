# Guardian configuration safety

| | |
|---|---|
| **Priority** | P1 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md) |
| **Open questions** | 4 (Open questions) |

**Why this priority**: two routes to a daemon that crashes after passing its
pre-flight checks, and one command that silently rewrites the operator's
configuration.
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP_PLAN.md)
findings 4, 5, 11, 29, 34.
**Already landed**: `config list` renders from the registry's own group order
and a test asserts every group appears (finding 10 — commits 41f6b90 and
ab11d86, `TestConfigListShowsEveryRegistryGroup`). Passphrase files are read
verbatim, never decoded (finding 30 — `custody.ReadPassphraseFile`, present
since the lift commit 142305e; the chain's devnet writes raw files to match).
Each command resolves its own configuration and a missing file is an error
rather than a message-and-success (commit 86bc865). `config validate` is folded
into `config doctor` (ab11d86). The `gas_price` default is derived from the
consensus floor ([done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md](done/DONE_GUARDIAN_NETWORK_SELECTION_PLAN.md)).
**Components**: `internal/config/config.go`, `manager.go`, `registry.go`;
`internal/cli/resolve.go`, `config.go`, `config_init.go`, `config_keys.go`,
`start.go`; `internal/chain/keys.go`, `signer.go`;
`internal/config/config_test.go`, `internal/cli/commands_test.go`.

---

## The issue

The configuration *registry* is well built — tag-derived, so file, environment
and `config set` share one parse path, with durations required to carry units.
The problems are all at its edges: what runs the validation, what the commands
do with the result, and which fields the validator forgot.

### 1. `guardiand start` never validates

`runStart` (`internal/cli/start.go:78`) resolves its configuration through
`requireConfig` (`internal/cli/resolve.go:39`), which loads the file, applies
environment overrides and flags, and never calls `Validate()`. The only callers
of `Config.Validate()` are `guardianctl config doctor`
(`internal/cli/config_doctor.go:84`) and the dashboard's `ConfigValid` field
(`internal/guardian/dashboard_source.go:426`).

Set-time checks are type-parse only, so
`guardianctl config set polling-interval 0s` succeeds. `config doctor` rejects
it; `start` never asks; `internal/guardian/service.go:413` then calls
`time.NewTicker(0)`, which **panics** — after the pre-flight checks have passed.
Every cross-field invariant the validator exists to enforce reaches a running
daemon, and a crash on a guardian holding accepted assignments is a no-reveal
slash.

### 2. A malformed `gas_price` panics at the first transaction

`internal/chain/signer.go:65` swallows the parse error with the comment
`// Validate() owns malformed configuration; this is advisory only`.
`Config.Validate()` contains no `gas_price` check. `signer.go:195` then calls
`WithGasPrices`, and cosmos-sdk v0.55.0's `client/tx/factory.go:188-192`
**panics** on a parse failure.

`guardianctl config set gas-price 0.1` (denom omitted) passes set, load, doctor
and startup, then panics the first time the daemon confirms or reveals —
precisely when an assignment is at stake, and under systemd it crash-loops
through the window.

Two related gaps on the same field: the `gas_price` denom is never cross-checked
against `denom`, and a price below the consensus floor
(`MinGasPriceUveilNum`/`MinGasPriceUveilDen`, `timeflareio/chain` —
`x/secrets/types/constants.go`, read here through `config.MinGasPrice()`) is not
flagged, so every transaction fails in CheckTx one at a time.

### 3. `config set` bakes transient environment overrides into the file

`runConfigSet` (`internal/cli/config.go:157`) does `requireConfig` → `Set` →
`Save()`. `requireConfig` calls `Manager.Load`, which applies
`ApplyEnvOverrides` into the in-memory config (`internal/config/manager.go:75`),
and `Save` serialises the **effective** values (`manager.go:95`). The same
pattern appears in `runConfigMigrateKey` (`internal/cli/config_keys.go:92`,
`:171`, `:180`).

Verified empirically on 1 August 2026, before the command moved to
`guardianctl`; the load-then-save shape is unchanged:
`GUARDIAN_POLLING_INTERVAL=99s guardianctl config set retry-attempts 5` writes
`polling_interval: 1m39s` permanently into the file.

This inverts the precedence the generated YAML header itself documents —
"flags > env > file > defaults" (`manager.go:116`).

### 4. A custom `--config-path` splits the keyring

`Manager.GetKeyDirectory()` (`internal/config/manager.go:189`) returns
`keyring_dir` when the operator chose one, and otherwise the *configuration
file's* directory for a non-default `--config-path`. `config init` uses it to
place the keyring passphrase file and to look the signing key up
(`internal/cli/config_init.go:502`, `:576`). But the runtime keyring opens
`cfg.KeyringDir` (`internal/chain/keys.go:117`), and `config init` writes
`keyring_dir` into the file only when `--keyring-dir` was passed
(`config_init.go:483`) — otherwise it stays the default `~/.timeflare/guardian`,
which is what `Save()` writes into the YAML.

So for `guardianctl config init --config-path /etc/guardian/config.yaml`, two
functions answer "where is the keyring?" differently: init looks beside the
configuration, while `guardianctl wallet create`, `register` and
`guardiand start` open the default directory. The chain's devnet only avoids it
because `timeflareio/chain` — `devnet/guardians.sh` always passes
`--keyring-dir`.

### 5. Validation gaps on numeric and duration fields

`Validate()` checks `polling_interval`, `request_timeout`, `retry_attempts`, the
`max_*` fields and the ports, but not:

| Field | Consequence of zero/negative |
|---|---|
| `retry_backoff` | `internal/chain/client.go:96` multiplies it; `time.After(≤0)` fires immediately, so retries hammer a struggling node with no spacing |
| `event_reconnect_backoff` | `internal/guardian/events.go:58` becomes a hot reconnect loop against a down endpoint |
| `shutdown_timeout` | `internal/cli/start.go:193` and `internal/monitoring/service.go:310` get an already-expired context; shutdown is never graceful |
| `gas_adjustment` | `CalculateGas` (`internal/chain/signer.go:214`) returns `uint64(adjustment × gasUsed)`, so non-reimbursed messages declare zero gas and are rejected; reveals survive only via the `reimbursedGasLimit` floor |
| `fee_buffer_percent` | Negative weakens the register balance pre-flight (`internal/guardian/registration.go:192`) |

Plus `gas_price` (issue 2), an unparsed free-form `stake_amount`, and no upper
bound on `polling_interval` — with event monitoring off, a large interval can
straddle the entire 50-block commit window (`CommitTimeoutBlocks`,
`timeflareio/chain` — `x/secrets/types/constants.go`) so assignments expire
unconfirmed.

`NewActiveSecretCache` (`internal/guardian/cache.go:72`) is the counter-example
done right: it clamps non-positive values defensively.

---

## Design

### Phase 1 — validate where it matters

`runStart` calls `cfg.Validate()` after `requireConfig` and before any chain
work, failing with the validator's message. `runConfigSet` validates the
resulting config after the set and refuses to save an invalid one, so the file
can never reach a state that `start` will reject.

There is a bootstrapping trap here. `requireConfig` already fails every command
on a file whose values do not *parse* — `Manager.Load` returns the registry's
error — including the `config set` needed to repair it, so that repair
currently requires hand-editing YAML. Adding validation in the same place would
extend the trap to every *invalid* file. Phase 1 must therefore decide which
commands refuse and which proceed — see open question 2.

### Phase 2 — validate `gas_price` properly

Add to `Validate()`:

- `sdk.ParseDecCoins(cfg.GasPrice)` must succeed and be non-empty.
- Its denom must equal `cfg.Denom`.
- Below the consensus floor is a hard error, not a warning: every transaction
  would fail in CheckTx, and a guardian that cannot transact is a guardian that
  gets slashed. (Above the floor stays permitted and stays the operator's own
  cost — `timeflareio/chain` — `docs/CHAIN_MECHANICS.md` Trade-off §17, with the
  existing startup warning in `warnIfPricedAboveReimbursement`,
  `internal/chain/signer.go:62`, unchanged.) See open question 4.

The comment at `signer.go:65` becomes true and can stay.

### Phase 3 — separate stored from effective configuration

`Manager` distinguishes the values read from file from the effective values
after environment overrides. `Save()` writes the former; everything else reads
the latter. `config set` mutates the stored layer.

`config list` should mark any key currently overridden by the environment, so
an operator can see why the running value differs from the file — the
information that makes the precedence rule visible rather than merely
documented.

### Phase 4 — one keyring directory

`GetKeyDirectory()` and `cfg.KeyringDir` must not be able to disagree. The
straightforward resolution: `config init` writes the resolved keyring directory
into the config it generates, so the directory init uses is the directory the
runtime opens. See open question 3 for the alternative.

### Phase 5 — close the validation gaps

Add positive-value checks for `retry_backoff`, `event_reconnect_backoff`,
`shutdown_timeout` and `gas_adjustment`; a non-negative check for
`fee_buffer_percent`; and parse `stake_amount` as a coin.

`polling_interval` is deliberately **not** bounded against a commit window here.
The obvious form — a fraction of `CommitTimeoutBlocks` converted to a duration —
needs a cadence this daemon does not carry: every window the protocol defines is
a block count, and a stored duration would be a second opinion about a network
the daemon can measure (`timeflareio/chain` —
`DONE_BLOCK_TIME_CONFIGURATION_PLAN.md`). `config init` sizes the interval from
the registry's cadence, which is the one place that knows it. A daemon-side bound
would have to be expressed in blocks, or against a measurement, and neither is
worth the machinery for a fallback poll rate whose primary discovery path is
event-driven.

Where a field has a safe fallback rather than a correct-by-refusal answer,
prefer clamping with a warning, following the cache's precedent.

---

## What this plan does not solve

- **It does not add hot reload.** No file watcher exists and none is proposed;
  configuration is read once at startup. What the plan should do is make that
  explicit — see open question 1 — rather than leave an operator believing a
  `config set` against a running daemon took effect.
- **It does not change the port-collision check** to compare host and port
  rather than port alone (finding 41). That false positive affects the
  remote-node topology and belongs to
  [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  phase 3.
- **It does not remove the dead configuration keys.** `enable_metrics`,
  `enable_health_check`, `log_file_path`, `max_concurrent_secrets` and
  `monitor_name` are
  [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)'s,
  because the decision for each is wire-or-delete rather than validate.
- **It does not touch the passphrase command-line flags.** Those are
  [PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md](PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md)
  phase 3.

---

## Open questions

1. **How should the daemon communicate that configuration changes need a
   restart?** Nothing currently says so: `config set` prints success, the YAML
   header does not mention it, and no doc does.
   *Recommendation: print it at the point of change.* `config set` should say
   the value takes effect on restart, and name whether a daemon is currently
   running if that is cheaply detectable. A watcher would be the alternative
   and is not worth its complexity — most of these values are wired into
   constructors at startup, so reloading them safely is a much larger change
   than it appears.

2. **Which commands refuse an invalid file, and how do the config commands
   survive one?** Every command resolves its own configuration, so the choice
   is per command. Today `requireConfig` fails all of them alike on an
   unparseable value, so an operator cannot repair a bad value with the tool.
   *Recommendation: validation is applied per command, and the repair commands
   load leniently.* Commands that act on the chain (`guardiand start`,
   `guardianctl register`, `update`, `rotate-key`) refuse on an invalid config;
   commands that exist to inspect and repair it
   (`guardianctl config get/set/list/doctor`) proceed and report the problem.
   That is the only arrangement in which the repair path works.

3. **Should `GetKeyDirectory()` exist at all?** Its "derive the keyring
   directory from the config path" behaviour is what diverges from
   `cfg.KeyringDir`.
   *Recommendation: keep the derivation but make it authoritative once, at
   `config init`, then delete the runtime helper.* Two functions answering
   "where is the keyring?" is the defect; resolving it at generation time and
   storing the answer leaves exactly one.

4. **Should a below-floor `gas_price` be a hard error or a warning?** A hard
   error is safer; a warning is friendlier to someone experimenting on a devnet
   with a modified floor.
   *Recommendation: hard error.* The floor is a consensus constant, not a
   preference, and a guardian that cannot transact is worse off than one that
   refuses to start.

---

## Related plans

- [PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md](PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md)
  — owns the passphrase *flags*.
- [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  — the `grpc_tls*` keys want the validation this plan establishes; it also
  owns the host:port collision check.
- [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
  — the wire-or-delete decisions for keys this plan deliberately leaves alone.
- [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
  — the restart-required semantics from open question 1 need documenting there.
- [PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md](PENDING_GUARDIAN_INFLIGHT_GUARD_RATIONALE_PLAN.md)
  — corrects the commit-window constant named in the in-flight guard's comment;
  phase 5 here cites the same window.
