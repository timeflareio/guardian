# Guardian configuration safety

**Priority**: P1 — two routes to a daemon that crashes after passing its
pre-flight checks, one command that silently shows nothing, and one that
silently rewrites the operator's configuration.
**Status**: refining (1 August 2026)
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
findings 4, 5, 10, 11, 29, 30, 34.
**Components**: `guardian/config/config.go`, `manager.go`, `registry.go`;
`guardian/cmd/guardiand/cmd/config.go`, `start.go`, `root.go`;
`guardian/blockchain/keys.go`; `guardian/config/config_test.go`,
`dashboard_config_test.go`; a new test file for the `cmd` package.

---

## The issue

The configuration *registry* is well built — tag-derived, so file, environment
and `config set` share one parse path, with durations required to carry units.
The problems are all at its edges: what runs the validation, what the commands
do with the result, and which fields the validator forgot.

### 1. `guardiand start` never validates

`cmd/guardiand/cmd/start.go:69-137` contains no `Validate()` call;
`root.go:51-63` only calls `LoadOrDefault`. The sole callers of `Validate()`
are `config validate`, `config doctor` and the dashboard display.

Set-time checks are type-parse only (`registry.go:102-111`), so
`guardiand config set polling-interval 0s` succeeds. `config validate` rejects
it; `start` never asks; `service.go:413` then calls `time.NewTicker(0)`, which
**panics** — after the pre-flight checks have passed. Every cross-field
invariant the validator exists to enforce reaches a running daemon, and a crash
on a guardian holding accepted assignments is a no-reveal slash.

### 2. A malformed `gas_price` panics at the first transaction

`blockchain/signer.go:64-66` swallows the parse error with the comment
`// Validate() owns malformed configuration; this is advisory only`.
`Config.Validate()` contains no `gas_price` check. `signer.go:197` then calls
`WithGasPrices`, and cosmos-sdk v0.53.4's `client/tx/factory.go:190-194`
**panics** on a parse failure.

`config set gas-price 0.1` (denom omitted) passes set, load, validate, doctor
and startup, then panics the first time the daemon confirms or reveals —
precisely when an assignment is at stake, and under systemd it crash-loops
through the window.

Two related gaps on the same field: the `gas_price` denom is never cross-checked
against `denom`, and a price below the consensus floor
(`x/secrets/types/constants.go:357-358`) is not flagged, so every transaction
fails in CheckTx one at a time.

### 3. `config list` displays nothing

`cmd/guardiand/cmd/config.go:846-858` hardcodes group names — `"Network
Configuration"`, `"Staking & Economics"`, `"Networking & Timeouts"`,
`"Registration Defaults"` and others — that match **zero** of the registry's
actual tag groups (`Network`, `Identity & Keys`, `Economics`, `Chain
Interaction`, `Service`, `Event Monitoring`, `Monitoring`). Every lookup misses
and is skipped.

Verified empirically against a fresh config: the output is a header, the config
path and a footer, with nothing between them. This is the command every flow
points operators at — `config init`'s next steps, `config set`'s help, the
no-config banner. `config doctor` works because it iterates
`config.GroupOrder()` (`config.go:171`). No test covers the `cmd` package,
which is how it drifted.

### 4. `config set` bakes transient environment overrides into the file

`config.go:787-808` does `Load()` → `Set` → `Save()`. `Load` applies
`ApplyEnvOverrides` into the in-memory config (`manager.go:66`), and `Save`
serialises the **effective** values (`manager.go:86-118`). The same pattern
appears in `runConfigMigrateKey` (`config.go:1097`, `:1106`).

Verified empirically:
`GUARDIAN_POLLING_INTERVAL=99s guardiand config set retry-attempts 5` wrote
`polling_interval: 1m39s` permanently into the file.

This inverts the precedence the generated YAML header itself documents —
"flags > env > file > defaults" (`manager.go:107`).

### 5. A custom `--config-path` splits the keyring

`manager.go:177-197` — `GetKeyDirectory()` returns the *config file's*
directory for custom paths, and `config init` prints
`timeflared keys add … --keyring-dir <that dir>` from it (`config.go:493-501`),
placing the encryption keys there. But the runtime keyring opens
`cfg.KeyringDir` (`blockchain/keys.go:126`), which — unless `--keyring-dir` was
explicitly passed — remains the default `~/.timeflare/guardian`, and that
default is what `Save()` writes into the YAML.

So an operator who runs `config init --config-path /etc/guardian/config.yaml`
and creates the signing key exactly where instructed gets `ErrKeyNotFound` from
`register` and `start`. The devnet only avoids it because `guardians.sh:171-173`
always passes `--keyring-dir`.

### 6. The passphrase file is decoded by guess

`blockchain/keys.go:97-107` attempts a base64 decode and uses the result "when
possible", falling back to raw text. The config field is documented simply as
"Path to a file containing the keyring passphrase" (`config.go:63`).

An operator who writes a raw passphrase — the natural reading — whose length is
a multiple of four over the base64 alphabet (`correcthorse`, `Passw0rd`, most
12- and 16-character alphanumerics) has it silently decoded to binary garbage.
The keyring then fails as "incorrect passphrase" with nothing indicating the
content was reinterpreted.

### 7. Validation gaps on numeric and duration fields

`Validate()` checks `polling_interval`, `request_timeout`, `retry_attempts`, the
`max_*` fields and the ports, but not:

| Field | Consequence of zero/negative |
|---|---|
| `retry_backoff` | `client.go:97` multiplies it; `time.After(≤0)` fires immediately, so retries hammer a struggling node with no spacing |
| `event_reconnect_backoff` | `events.go:58` becomes a hot reconnect loop against a down endpoint |
| `shutdown_timeout` | `start.go:187` and `monitoring/service.go:309` get an already-expired context; shutdown is never graceful |
| `gas_adjustment` | `CalculateGas` returns `uint64(adjustment × gasUsed)`, so non-reimbursed messages declare zero gas and are rejected; reveals survive only via the `reimbursedGasLimit` floor |
| `fee_buffer_percent` | Negative weakens the register balance pre-flight (`registration.go:192`) |

Plus `gas_price` (issue 2), an unparsed free-form `stake_amount`, and no upper
bound on `polling_interval` — with event monitoring off, a large interval can
straddle the entire 20-200-block commit window so assignments expire
unconfirmed.

`NewActiveSecretCache` (`cache.go:73-78`) is the counter-example done right: it
clamps non-positive values defensively.

---

## Design

### Phase 1 — validate where it matters

`runStart` calls `cfg.Validate()` after load and before any chain work, failing
with the validator's message. `runConfigSet` validates the resulting config
after the set and refuses to save an invalid one, so the file can never reach a
state that `start` will reject.

There is a bootstrapping trap here: `initConfig` already exits 1 for *every*
command on an invalid file (`root.go:56-59`), including the `config set` needed
to repair it, so repair currently requires hand-editing YAML. Phase 1 must
therefore also make the config commands survive an invalid load — see open
question 2.

### Phase 2 — validate `gas_price` properly

Add to `Validate()`:

- `sdk.ParseDecCoins(cfg.GasPrice)` must succeed and be non-empty.
- Its denom must equal `cfg.Denom`.
- Below the consensus floor is a hard error, not a warning: every transaction
  would fail in CheckTx, and a guardian that cannot transact is a guardian that
  gets slashed. (Above the floor stays permitted and stays the operator's own
  cost — CHAIN_MECHANICS.md Trade-off §17, with the existing startup warning at
  `signer.go:63-83` unchanged.)

The misleading comment at `signer.go:64-66` becomes true and can stay.

### Phase 3 — fix `config list`

Delete the hardcoded slice and iterate `config.GroupOrder()`, exactly as
`config doctor` does. Then add the missing `cmd`-package test coverage that
would have caught it: a golden test asserting that `config list` emits every
registry key, which fails by construction if the two ever diverge again.

### Phase 4 — separate stored from effective configuration

`Manager` distinguishes the values read from file from the effective values
after environment overrides. `Save()` writes the former; everything else reads
the latter. `config set` mutates the stored layer.

`config show`/`list` should mark any key currently overridden by the
environment, so an operator can see why the running value differs from the
file — the information that makes the precedence rule visible rather than
merely documented.

### Phase 5 — one keyring directory

`GetKeyDirectory()` and `cfg.KeyringDir` must not be able to disagree. The
straightforward resolution: `config init` writes the resolved keyring directory
into the config it generates, so the value the instructions print is the value
the runtime opens. See open question 3 for the alternative.

### Phase 6 — unambiguous passphrase files

Read the passphrase file as raw bytes, trimmed of trailing newline, with no
decoding. The devnet's base64 storage is updated to write the raw passphrase
(it controls both ends).

This is a behavioural change for any existing base64 file, which is acceptable
pre-launch and preferable to a heuristic that silently corrupts valid
passphrases. See open question 4 for the compatibility alternative.

### Phase 7 — close the validation gaps

Add positive-value checks for `retry_backoff`, `event_reconnect_backoff`,
`shutdown_timeout` and `gas_adjustment`; a non-negative check for
`fee_buffer_percent`; and parse `stake_amount` as a coin.

`polling_interval` is deliberately **not** bounded against a commit window here.
The obvious form — a fraction of `MinCommitTimeout × block_time` — needs a cadence
this daemon does not carry: `block_time` is gone, because every window the protocol
defines is a block count and a stored duration would be a second opinion about a
network the daemon can measure (the chain's
`PENDING_BLOCK_TIME_CONFIGURATION_PLAN.md`). `config init` sizes the interval from
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
  remote-node topology and belongs with the transport work.
- **It does not remove the dead configuration keys.** `enable_metrics`,
  `enable_health_check`, `log_file_path`, `max_concurrent_secrets` and
  `monitor_name` are
  [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)'s,
  because the decision for each is wire-or-delete rather than validate.
- **It does not touch the passphrase command-line flags.** Those are
  [PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md](PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md)
  phase 3; this plan owns only how the passphrase *file* is read.

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

2. **How do the config commands survive an invalid file?** They currently
   cannot, so an operator cannot repair a bad value with the tool.
   *Recommendation: `initConfig` loads without validating, and validation is
   applied per command.* Commands that act on the chain (`start`, `register`,
   `update`, `rotate-key`) refuse on an invalid config; commands that exist to
   inspect and repair it (`config get/set/list/validate/doctor`) proceed and
   report the problem. That is the only arrangement in which the repair path
   works.

3. **Should `GetKeyDirectory()` exist at all?** Its "derive the keyring
   directory from the config path" behaviour is what diverges from
   `cfg.KeyringDir`.
   *Recommendation: keep the derivation but make it authoritative once, at
   `config init`, then delete the runtime helper.* Two functions answering
   "where is the keyring?" is the defect; resolving it at generation time and
   storing the answer leaves exactly one.

4. **Do we need backward compatibility for base64 passphrase files?**
   *Recommendation: no.* Pre-launch breaking changes are permitted, the devnet
   controls both ends, and any compatibility shim reintroduces the ambiguity.
   If a transition period is wanted, an explicit `base64:` prefix would be the
   way — never a guess.

5. **Should a below-floor `gas_price` be a hard error or a warning?** A hard
   error is safer; a warning is friendlier to someone experimenting on a devnet
   with a modified floor.
   *Recommendation: hard error.* The floor is a consensus constant, not a
   preference, and a guardian that cannot transact is worse off than one that
   refuses to start.

---

## Related plans

- [PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md](PRIORITY_GUARDIAN_CUSTODY_HARDENING_PLAN.md)
  — owns the passphrase *flags*; this plan owns the passphrase *file* reader.
- [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
  — adds TLS configuration keys, which will want the validation this plan
  establishes; also owns the host:port collision check.
- [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
  — the wire-or-delete decisions for keys this plan deliberately leaves alone.
- [PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md](PENDING_GUARDIAN_DOCS_RESYNC_PLAN.md)
  — the restart-required semantics from open question 1 need documenting there.
