# Daemon Surface Separation — `timeflareio/guardian` — Plan

*Separates the daemon's runtime path from the operator's setup path, and adopts
the conventional Go layout that makes the separation enforceable rather than
merely intended.*

> **Status: in progress** — created and ruled 3 August 2026; all five phases
> executed on `worktree-daemon-surface-separation` the same day. What remains is
> the chain-side half of §4 (see *Residual*, below), which is another
> repository's change and not this branch's to make.
> **Priority**: P2 — layout and separation debt. Cheapest to pay now (nothing
> imports this module, so no consumer is disturbed), and it is what unblocks the
> test coverage standing between an operator and key loss. Argued below (§1) why
> that coverage argument does not make it P1.
> **Origin**: repository layout review requested by the owner, 3 August 2026.
> **Components**: every package in the module (`config/`, `custody/`,
> `blockchain/`, `guardian/`, `monitoring/`, `dashboard/`, `utils/`),
> `cmd/guardiand/`, the new `cmd/guardianctl/`, `Makefile` +
> `make/go-build.mk` + `make/go-quality.mk`, `Dockerfile`,
> `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `CLAUDE.md`,
> `testdata/vectors/` (test-relative paths only), the chain repository's devnet
> scripts, and [PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md)
> §1 (edited as a Phase 5 deliverable — see §4).

## 1. The concern

`guardiand` has one undifferentiated surface. The command that runs the service
and the commands that generate, export and rewrite key material share a package,
a set of mutable package globals, and a printing layer wired directly to
`os.Stdout`. Three consequences, in descending order of severity:

**The command layer cannot be tested.** [`cmd/guardiand/cmd/`](../../cmd/guardiand/cmd/)
is ~3,500 lines across 13 files with zero test files. It contains the key
generation, backup, restore and rotation-cutover orchestration — the code whose
failure modes are "the guardian misses every reveal encrypted to that key". The
`custody/` primitives beneath it are well tested; the orchestration around them
is not, and cannot be while output goes to a package-level `fmt.Printf` and
config arrives through a mutable global.

**The daemon inherits the setup path's manners.** `guardiand start` prompts for
interactive confirmation by default ([`start.go:80`](../../cmd/guardiand/cmd/start.go#L80));
`--accept` is the escape. A service under a process supervisor must never have an
interactive gate. `cobra.OnInitialize` loads config for *every* invocation and
calls `os.Exit(1)` on failure ([`root.go:49-61`](../../cmd/guardiand/cmd/root.go#L49-L61)),
so `guardiand version` fails on a host with no config file.

**Two views of config coexist in one process.** `start`, `register`, `update`,
`health` and `status` read the package global `cfg`; `key`, `wallet` and
`rotate-key` call `cfgManager.Load()` and read `GetConfig()`. They can disagree,
and in one path they demonstrably do: `runConfigInit` replaces `cfgManager`
mid-run ([`config.go:405-413`](../../cmd/guardiand/cmd/config.go#L405-L413))
while the global `cfg` still points at what `OnInitialize` produced. This is a
correctness hazard, not a style preference.

Underneath all three is the layout: every package sits at the module root and is
publicly importable, so "the daemon path must not touch the setup path" is a
statement no tool can check. `internal/` makes it a compile error. That is why
the layout move leads rather than trails — it is the mechanism, not cosmetics.

**Why not P1.** The untested key-handling command layer is the strongest
argument for urgency, but this plan does not *itself* fix a live defect: it makes
the tests writable (Phase 4 writes them). No third party runs `guardiand` yet
(see [PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md) §1),
so the exposure is potential rather than realised. If the owner weighs
key-loss risk above that reasoning, P1 is defensible.

## 2. Target layout

| Today | After | Why |
|---|---|---|
| `config/` | `internal/config/` | not importable by anything outside this binary |
| `custody/` | `internal/custody/` | as above |
| `blockchain/` | `internal/chain/` | "chain" is the vocabulary the sibling repository and the spec use; `blockchain` is a word nobody uses as a package name |
| `guardian/` | `internal/guardian/` | moved, **not** renamed — see below |
| `guardian/mocks/` | `internal/guardian/mocks/` | travels with its package |
| `monitoring/` | `internal/monitoring/` | moved |
| `dashboard/` | `internal/dashboard/` | moved; the embedded `assets/` subtree travels with it |
| `utils/shutdown.go` | `internal/guardian/shutdown.go` | a package holding one function, named for nothing |
| `cmd/guardiand/cmd/` | `internal/cli/` | the doubled `cmd` is a Cobra-generator habit; `main.go` calls `cli.Execute()` |
| — | `internal/cli/ui/` | the printing and prompting layer, extracted (Phase 3) |

`internal/guardian` keeps its name deliberately, and keeps it after Phase 5. The
package holds the guardian's behaviour, and both binaries need some of it: the
daemon takes the reveal loop, the cache and the event monitor; `guardianctl`
takes the registration manager and the status query. Renaming it `daemon` would
mislabel the half that ships in the other binary. The stutter that matters —
`utils`, `blockchain` — goes; a single `guardian/internal/guardian` repetition in
an import path is ordinary Go.

**One new component, approved.** Every package above is an existing package moved
or renamed; `internal/cli/ui` is [`output.go`](../../cmd/guardiand/cmd/output.go)
plus the prompt helpers that today sit in `config.go`, relocated. The only
genuinely new component is the second binary, `cmd/guardianctl`, approved by the
owner on 3 August 2026 under `README.md`'s architectural-minimalism rule. The
case is in §4, Phase 5.

## 3. The boundary this establishes

After Phase 3, three rules hold and two of them are machine-checked:

1. **Nothing outside this module can import any of it** — `internal/`, enforced
   by the compiler.
2. **Nothing outside `internal/cli/ui` writes to stdout** — enforced by a new
   `verify-boundaries` check. This is achievable cleanly today: all four
   `fmt.Print*` call sites in the module are already inside `output.go`.
3. **The daemon process never mutates or exports key material** — `start` loads,
   validates and runs; it does not call `Manager.Save()`, does not prompt, and
   does not write, seal or export a key. Held by convention after Phase 3, and
   by construction after Phase 5, when the code that could do any of it is no
   longer linked into `guardiand` at all.

## 4. Phases

Each phase ends green on `make verify && make test` and is a separate commit;
Phases 1–4 land in one worktree forked from `main`.

### Phase 1 — conventional layout (mechanical, no behaviour change)

The moves in §2, plus the import-path rewrite. Deliberately behaviour-preserving
so the diff is reviewable as a rename: no logic changes ride along.

Known gotchas, each a required edit rather than a risk:

- `blockchain/wallet_key_test.go:38` and `custody/mnemonic_vectors_test.go:32`
  read the vendored chain vectors via `filepath.Join("..", "testdata", ...)`.
  One directory deeper, both become `"..", ".."`. The `vectors-verify` and
  `vectors-sync` targets resolve from the repository root and are unaffected.
- `//go:embed assets` in `dashboard/handler.go:15` is package-relative, so it
  survives the move untouched — confirm rather than assume, because a silent
  failure here degrades to a blank dashboard page rather than a build error
  (`pageFS` falls back to `emptyFS` by design).
- `CMD_PATH ?= ./cmd/$(APPNAME)` (`make/go-build.mk`), the `Dockerfile` build
  path and `release.yml:91` all name `./cmd/guardiand`, which does not move.
- `verify-boundaries` is unchanged in this phase: it guards against chain
  internals, which is an orthogonal concern to guardian-internal layering.
- `.golangci.yml` is path-agnostic — confirm no exclusion references a moved
  directory.
- `CLAUDE.md:14` names `custody/` as the path whose changes must be read against
  `GUARDIAN_KEY_CUSTODY.md`. That reference and any layout prose need updating.
  The custody guide itself contains no path references — verified 3 August 2026 —
  so it needs no sweep for this phase.

*Acceptance*: `make verify && make test` green; the diff is moves, import
rewrites and the two test-path fixes, nothing else.

### Phase 2 — one config resolution point, no globals

Delete `configPath`, `cfg`, `cfgManager` and `cobra.OnInitialize(initConfig)`.
Replace with a single resolver in `internal/cli` that each command calls
explicitly, returning a resolved config to the command rather than publishing
one. Commands divide into three declared classes:

- **needs an existing config** — `start`, `register`, `update`, `status`, `key`,
  `wallet`, `rotate-key`, `config get|set|list|validate|doctor`,
  `config set-dashboard-password`. One "no configuration found" message, one
  place, no `os.Exit` from an initialiser.
- **must work without one** — `version`, `help`, `config init`. These never
  touch the loader. `config init` resolves its target path once, up front, and
  never replaces the manager mid-run.
- **needs one only conditionally** — falling back to defaults plus environment
  overrides. `health` reads `HealthPort` unless `--url` is given, so `--url` is
  made self-sufficient. The `wallet` verbs belong here too, and the reason is the
  documented setup order: `wallet create` runs *before* `config init`, because
  init resolves the guardian's address from the signing key. Requiring a
  configuration file would make the first step of a new guardian impossible.
  `config create-encryption-key` is the same case — it can write into a named
  directory before any configuration exists.

Then wire the flag layer that the generated YAML header and `ApplyEnvOverrides`
already advertise. Today "flags > env > file > defaults" has no flag layer at
all: every `Set*` call site in `cmd/` persists a *setup* value, and no command
pushes a flag into a runtime field. The tag-driven registry makes the fix
generic rather than per-field — each field already has a key, a parser and a
setter, so binding is one loop. Bind at minimum `--chain-id`, `--rpc-endpoint`,
`--grpc-endpoint`, `--log-level`, `--log-format`, `--bind-address`,
`--metrics-port`, `--health-port`, `--dashboard-port` and `--polling-interval`
on `start`. Either the claim becomes true or it comes out of the YAML header;
it does not stay aspirational.

Remove the interactive confirmation from `start`, and remove the `--accept` flag
with it. A service under a process supervisor has no terminal to confirm at, so
the prompt is not a safety feature — it is a startup failure waiting for a host
without a TTY.

The flag goes too, but in a second step, because the chain repository's devnet
scripts invoke `guardiand start --accept` and there is no single-step order that
does not break them. Removing prompt and flag together makes the existing
invocation an unknown-flag error; dropping the flag from the devnet first makes
today's binary prompt, read EOF from a non-terminal stdin, and *cancel startup* —
which is the worse of the two, because `runStart` returns nil on a declined
prompt, so the devnet's guardian exits zero without ever starting.

Split across the plan instead, which has no broken window in either repository:

1. **Phase 2, here** — remove the prompt. `--accept` survives as a hidden flag
   that is accepted and ignored, so every existing invocation keeps working.
2. **Chain repository** — drop `--accept` from the devnet invocations, against a
   `guardiand` carrying step 1. Whether that is a tagged release or a local build
   depends on how the devnet obtains the binary, which is settled against the
   chain checkout during execution.
3. **Phase 5, here** — delete the flag. Phase 5 already carries a chain-side
   sweep for the `guardianctl` verb split, so this rides in the same coordinated
   change rather than adding one.

`--accept` on `register`, `update` and `config migrate-key` is unaffected. Those
are operator verbs run at a terminal, where a confirmation prompt before signing
a transaction or encrypting a key in place is the point.

Separate `internal/config`'s read surface from its mutating surface in the same
pass: the daemon path gets load-and-validate, and `Manager.Save()` is reachable
only from setup commands. This is the seam Phase 5 splits on, so it is worth
drawing now even if Phase 5 never lands.

*Acceptance*: `guardiand version` succeeds with no config file present;
`guardiand start` never prompts; `GUARDIAN_LOG_LEVEL=debug` and `--log-level
debug` both take effect with the flag winning; no package-level config variable
survives a grep.

### Phase 3 — quarantine the interactive surface

- `internal/cli/ui` takes the colour definitions, indentation constants, message
  helpers, `promptForInput`, `promptForConfirmation`, `promptNewPassphrase` and
  `readPasswordInput` (which today sits in `config.go`, the wrong home for a
  terminal primitive that installs a signal handler).
- Every writer comes from `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`. This is the
  change that makes Phase 4 possible.
- Honour `NO_COLOR` and non-TTY explicitly rather than relying on
  `fatih/color`'s own detection — the daemon's startup banner goes to a
  container log, where escape codes are noise.
- Add the stdout check from §3.2 to `verify-boundaries`.
- Split [`config.go`](../../cmd/guardiand/cmd/config.go) (1,331 lines) into
  `config.go` (group, get, set, list, validate), `config_init.go` (the wizard),
  `config_doctor.go` and `config_keys.go` (`create-encryption-key`,
  `migrate-key`). `config_dashboard_password.go` stays as it is.

*Acceptance*: no `fmt.Print*` outside `internal/cli/ui`; a command's full output
is capturable with `cmd.SetOut`; `verify-boundaries` fails if either rule is
broken.

### Phase 4 — test the command layer

Now writable. Ordered by consequence, not by ease:

1. **`key backup` → `key restore` round trip**, including retired epoch keys,
   `--offline`, `--force` refusal, and the wrong-key rejection path (a restored
   key whose derived public key does not match the registered record must fail,
   loudly).
2. **`rotate-key` cutover failure modes** — the staged `.next` file is removed
   when the broadcast fails; the two renames leave a coherent layout; an
   unconfirmed epoch aborts without touching local keys. The existing
   `guardian/mocks` client covers the chain side.
3. **`config init` in flag mode** — exact file layout and permissions (0600
   config, 0600 key, 0600 passphrase file, 0644 hex), and the refusal to
   overwrite an existing config.
4. **`config migrate-key`** — plaintext to envelope, including the
   decrypt-and-compare verification after the write.
5. **Resolution precedence** — a table test over defaults / file / env / flag.
6. **`config doctor`** — each failure branch reports and exits non-zero.

*Acceptance*: every command has at least one test; the four key-touching
commands have failure-path tests. No numeric coverage target — it would be
gamed by testing the printing.

### Phase 5 — two binaries

- `cmd/guardiand` — `start`, `health`, `version`
- `cmd/guardianctl` — `config`, `wallet`, `key`, `register`, `update`,
  `rotate-key`, `status`, `version`

Both link the same `internal/` packages, so this is a packaging change over one
shared module, not a second implementation.

**What it buys, precisely.** `guardiand` contains no code that can mint, export
or rewrite key material: no `crypto.GenerateKeypair`, no `custody.SealBundle`, no
`SaveEncryptedShareKey`, no `Manager.Save`. `key backup` seals the entire epoch
keyring into one portable file, and that verb not being reachable from the
long-running process is the whole case. The daemon is the only component with
network-facing attack surface — the dashboard listener and the event WebSocket —
so it is the one that benefits from having no export path linked in.

**What it does not buy.** The daemon still reads the signing keyring (it signs
confirmations and reveals) and still decrypts the share key. It is not "the daemon
holds no key material" — it is "the daemon cannot write or export key material".
Overclaiming this would be worse than not splitting. It also does not clear the
accepted `openpgp` advisory in `.govulncheck-accepted`, which arrives via the SDK
keyring the daemon keeps.

**`register` changes shape.** It currently builds a whole `guardian.Service` —
event monitor, cache, reveal service — to reach one method
([`register.go:129-139`](../../cmd/guardiand/cmd/register.go#L129-L139)).
`update` already does the right thing: chain client, one call, no Service
([`update.go:530-536`](../../cmd/guardiand/cmd/update.go#L530-L536)). Make
`register` mirror it by constructing `NewRegistrationManager` directly, which
`Service.RegisterWithOptions` only delegates to anyway. Without this the split is
nominal — the operator binary would drag the entire daemon loop behind one verb.

**The image ships both binaries**, with `ENTRYPOINT ["guardiand"]` unchanged;
`guardianctl` is reached via `--entrypoint`. Operators need `register`,
`rotate-key` and `key backup` on the host regardless, and two images would double
the pull and version-match burden. This does not weaken the property above,
because the property is about what is linked into the running process, not what
sits on the image filesystem: an attacker who can start a second process in the
container already holds Docker-socket access and could read the mounted key files
directly.

**What it costs.** A second artefact set in `release.yml` plus checksums; a
version-match obligation between the pair; a chain-repository sweep for
`guardiand` verb invocations that must become `guardianctl`; and an edit to
[PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md) §1, whose
artefact table names one binary. That edit is a deliverable of this phase.

*Acceptance*: `go tool nm` on `guardiand` shows no `SealBundle`,
`GenerateKeypair` or `Manager.Save` symbol; `register` succeeds against the mock
client without constructing a `Service`; both binaries build from the same
`internal/` tree; the release workflow publishes and checksums both; the chain
devnet drives the pair.

`guardianctl` linking daemon internals is not a defect and is not checked —
`status` reads the active-secret cache, so it necessarily links it. The symbol
check runs one way, on the binary where the claim lives.

### Sequencing against the release strategy

The two plans do not block each other, and the ordering question dissolves once
the real constraint is named: **Phase 5 must land before the first release a
third party is asked to run.** Removing `key backup` from `guardiand` after
operators have deployed it is a migration — they would have the verb, then lose
it, and every runbook naming `guardiand key backup` would need reissuing. Before
anyone deploys, it is just a build change.

[PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md) already gates
its own third-party-facing release on §5.1 (binary signing, which it calls
blocking), and that question is independent of layout. So both plans proceed in
parallel: this one runs Phases 1–5 unblocked, and Phase 5 edits that plan's
artefact table when the second binary is real rather than speculated. Nothing is
lost if a `v0.0.x` tag ships in the meantime with one binary — this repository is
0.x with no third-party operators, which is the same reasoning that plan uses for
its own interim tags.

### Residual — the chain repository

Everything in Phases 1–5 that belongs to this repository has landed. Two changes
in `timeflareio/chain` remain, and they are why the devnet is not yet driving the
split pair:

1. **`devnet/guardians.sh:370`** passes `guardiand start --accept`. The flag is
   still accepted and ignored here, so nothing is broken today — but it is
   step 2 of the three-step removal above, and the flag cannot be deleted from
   this repository until it has landed.
2. **`devnet/guardians.sh:271` and `devnet/docker/init-guardians.sh:203`** invoke
   `guardiand register`, which is now a `guardianctl` verb. Until these are
   repointed the devnet's guardians will not register. `--accept` on `register`
   is unaffected and stays.

Both are confirmed against the chain checkout rather than assumed. Neither is
this branch's to make: they are a companion pull request in that repository,
and until it lands the compose devnet needs the previous guardian image.

## 5. Cross-component sweep

Per `README.md`'s rename/removal rule, the audit is grep-driven and confirms
which components are *clear*, not only which change:

| Target | Where | Phase |
|---|---|---|
| `github.com/timeflareio/guardian/{config,custody,blockchain,guardian,monitoring,dashboard,utils}` | all `*.go` | 1 |
| `filepath.Join("..", "testdata"` | 2 test files | 1 |
| `//go:embed` | `dashboard/handler.go` | 1 |
| `./cmd/guardiand` | `Dockerfile`, `release.yml`, `make/go-build.mk` | 1, 5 |
| `cfgManager`, package-level `cfg` | `cmd/` | 2 |
| `fmt.Print` | all packages | 3 |
| `custody/` and layout prose | `CLAUDE.md` | 1 |
| `guardiand config`/`start` invocations in help text | `Makefile` (`config-help`, `status`) | 2, 5 |
| `guardiand start --accept` | chain repository `devnet/`, compose files | between 2 and 5 |
| `guardiand` verb invocations that become `guardianctl` | chain repository `devnet/`, compose files | 5 |
| single-binary artefact table | [PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md) §1 | 5 |
| path references | `docs/guides/GUARDIAN_KEY_CUSTODY.md` — **clear as of 3 August 2026**, re-check after Phase 3 splits files | 3 |

The two chain-repository rows cannot be settled from this checkout, and they are
the only rows that can break another repository. Both are confirmed against the
chain checkout during execution, not guessed — including how the devnet obtains
its `guardiand`, which determines when the `--accept` edit can land (Phase 2).

## 6. Open questions

**None outstanding** — ruled 3 August 2026 and folded into the body: the second
binary is approved (§2, Phase 5), `start`'s `--accept` is removed rather than
retained (Phase 2 removes the prompt, Phase 5 the flag), and the sequencing
against the release strategy is settled in §4.

The case against the split is worth keeping on the record, since it did not go
away by being overruled: single-binary-with-subcommands is the convention in this
ecosystem — `timeflared start` and `timeflared keys add` are one binary, and
operators already run it — and the pair must stay version-matched on hosts that
need both present anyway. The ruling accepts that cost in exchange for the
property in Phase 5: the process holding the epoch keyring, and the only one with
network-facing surface, cannot export it.

## 7. What this plan does not solve

- **The `Config` key-cache layering inversion.** `Config` lazily decrypts and
  caches the share key (`GetEncryptionPrivateKey`, `GetRetiredEpochKey`,
  `WipeEncryptionKey`), so every component holding a `*config.Config`
  transitively holds the ability to decrypt shares. Key resolution belongs
  behind a custody-owned resolver. That is a change to key handling, where
  `docs/guides/GUARDIAN_KEY_CUSTODY.md` is the authority, so it needs its own
  plan rather than riding along here. This plan moves the code without changing
  its behaviour.
- **`Manager`'s path-derivation heuristics.** `GetKeyDirectory` and
  `GetPrivateKeyPath` decide by comparing against default values, and the
  result already misfires: `rotate_key.go:874-876` deliberately bypasses
  `GetPublicKeyPath` with a comment noting it resolves to the wrong directory.
  Same custody-adjacent plan.
- **Protocol behaviour.** Nothing here touches the wire contract, the carried
  pins, or the vendored vectors, so `README.md`'s spec-first obligation is not
  triggered. `verify-pins`, `vectors-verify` and `verify-boundaries`' chain-
  internals check all continue to pass unchanged.
- **Makefile hygiene.** `make clean` means "apply formatting and lint fixes"
  while `clean-all` and `clean-bin` remove artefacts, inverting the universal
  convention; and `common.mk` passes cosmos-sdk `-X version.*` ldflags that the
  `Dockerfile` deliberately omits, so `make build` and the image stamp
  differently. Both are unrelated to this concern and want a hygiene plan.
- **What `CollectKeyringFiles` sweeps into a bundle.** `keyring_dir` defaults to
  the guardian's whole data directory, so a backup bundle contains `config.yaml`,
  the at-rest passphrase sibling and the keyring passphrase — and anything else
  left there, including a backup passphrase file an operator stores beside the
  bundle, which would put a bundle's own passphrase inside it. Two consequences
  fall out: the "store them separately" guidance is load-bearing in a way nothing
  enforces, and `key restore` effectively always needs `--force`, because the
  configuration file it requires in order to run is itself in the bundle. Found
  while writing the round-trip test, which now keeps bundles off-host as the
  guidance says. Bundle composition is key custody, so it wants its own plan.
- **`create-encryption-key` and `config init` disagree on names.** init writes
  `private_key` and `public_key.hex`; `create-encryption-key --file-name X`
  writes `X.key` and `X.hex`. The two layouts never meet, so the standalone
  command cannot produce the files the configuration points at. A papercut, not a
  defect, and adjacent to the path-derivation heuristics above.
- **`status` exits zero when it reports an unhealthy guardian.** That is by
  design and stays: `status` is a human report, and `health` is the command that
  documents a non-zero exit for scripts and supervisors. Noted because it looks
  like the `start` defect this plan fixed and is not one.
- **Dashboard exposure, TLS and authentication** — unchanged throughout.
- **Operator documentation.** This repository has no install or run guide, and
  this plan does not create one — while changing the interface such a guide would
  describe twice over: Phase 2 alters `start` (no prompt, no `--accept`, new
  flags) and Phase 5 moves seven verbs to a second binary. Nothing external
  documents those today, which is the only reason this is a gap rather than a
  breaking change. It stops being true the moment a third party is asked to run
  `guardiand`, so the guide is owed before the release
  [PENDING_RELEASE_STRATEGY_PLAN.md](PENDING_RELEASE_STRATEGY_PLAN.md) §5.1
  gates, and wants its own plan.
