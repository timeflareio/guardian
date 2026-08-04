# Guardian network selection

**Priority**: P1 — `config init` never asks which network the guardian is for, so
every guardian comes out of setup pointed at the local devnet under the devnet's
chain id. Pre-testnet: the first public network is the moment this becomes an
operator-facing defect rather than a latent one.
**Status**: in progress (4 August 2026) — `worktree-network-selection`
**Origin**: [PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md](PENDING_GUARDIAN_PRE_TESTNET_SWEEP.md)
finding 22 and its `chain_id` docs item; the chain's network registry
(`chain/networks.json`, `chain/docs/guides/NETWORKS.md`); scope ruled by the
owner, 4 August 2026.
**Components**: `internal/config/networks.go` (new file, existing package),
`internal/config/config.go`, `internal/cli/config_init.go`,
`internal/cli/config_doctor.go`, `internal/chain/client.go`,
`internal/cli/key.go`, `internal/cli/register.go`, `internal/chain/signer.go`,
`internal/cli/config_init_test.go`, `internal/cli/config_doctor_test.go`,
`internal/config/config_test.go`, a new `internal/config/networks_test.go`,
`docs/guides/` operator setup text, `CLAUDE.md`,
[PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md);
and in the chain repository, `devnet/guardians.sh` and
`devnet/docker/init-guardians.sh`.

---

## The issue

The guardian's network identity is three hardcoded strings in
`internal/config/config.go:136-138`:

```go
ChainID:      "timeflare-test",
RPCEndpoint:  "http://localhost:26657",
GRPCEndpoint: "localhost:9090",
```

Nothing prompts for them. `runConfigInit` (`internal/cli/config_init.go:183-252`)
collects the signing key name, the keyring passphrase, the guardian address, the
share-encryption key and the dashboard password, and never touches the Network
group. An operator setting up for a public network is not asked, not warned, and
gets a configuration that *looks* complete.

The failure mode is the one finding 22 names: queries against a wrong node either
work or fail visibly, but a wrong `chain_id` produces working queries and
universally failing transactions. A real-looking wrong value is worse than an
empty one, and `timeflare-test` is a real-looking wrong value everywhere except
the devnet. A guardian that discovers this after bonding misses every reveal
window it accepted.

The three fields are also the wrong unit of configuration. They are only
meaningful together — an endpoint pair belonging to one chain id — and the chain
id is simultaneously the hardest of the three to guess and the least forgiving
when wrong. Asking an operator to type three related strings correctly is asking
for the one class of mistake that costs bond.

## What the chain publishes

`chain/networks.json` is the chain's own definition of the networks it runs as,
documented in `chain/docs/guides/NETWORKS.md`. Each entry carries `id`, `label`,
`chainId`, `local` and `endpoints` (`rpc`, `rest`, `grpc` — each a list), with a
top-level `default` naming the entry a consumer picks when the operator has not,
and an `addressPrefix`. The chain gates it with `make verify-networks`, which pins
`addressPrefix` to `app.go` and the devnet `chainId` to the devnet scripts.

**`local` is the transport statement as well as the locality one** (ruled by the
owner, 4 August 2026). `NETWORKS.md` permits cleartext for a loopback-scoped
network and nowhere else, and `verify-networks` enforces exactly that: a `local`
entry's `rpc` and `rest` must be loopback, a non-local entry's must be `https`. So
"is this network loopback-scoped" and "may this connection be in clear" are one
question with one answer, and a second field stating it separately could only ever
restate it or contradict it.

It is deployment fact rather than protocol: `chain/docs/spec.md` remains the
authority for what the protocol does, and a change to the registry never needs a
protocol release train.

Only the devnet is defined today, deliberately — a row naming a host that does
not answer would be handed to consumers as a default.

## The design

**Read the published list at `config init`, let the operator pick, write the
chosen values into the configuration file.** Nothing else changes.

That last sentence is the whole shape of it. The registry is consulted once, by
`guardianctl`, at setup, and **no code outside `guardianctl config init` knows a
registry exists**. The daemon reads `chain_id`, `rpc_endpoint` and `grpc_endpoint`
from its configuration, as it does today; a guardian already configured is
unaffected, and there is no migration.

The one place the daemon does change is the dialler, and only because a network
that is not loopback needs TLS before it can be reached at all — see "Reaching a
network that is not loopback". That reads a configuration field like any other; it
does not reach for the registry.

### Where the list is read from

One constant:

```go
// Where the chain publishes the networks it runs as. Read at `config init` to
// offer them for selection; the values chosen are written into the guardian's
// configuration and are the daemon's only source thereafter.
const NetworkListURL = "https://raw.githubusercontent.com/timeflareio/chain/main/networks.json"
```

This will move to a TLS-served path on a `timeflare.io` domain once one exists
(ruled by the owner, 4 August 2026). That is a one-line change to this constant
and nothing else, which is why the first pass deliberately carries no vendored
copy, no pinned tag and no `networks-sync` target: pinning a location that is
about to move buys a guarantee about the wrong thing. `raw.githubusercontent.com`
is already https, so the transport posture is the same before and after the move;
what changes is who operates the host.

`FetchNetworkList` takes the URL as a parameter rather than reading the constant
directly, so tests drive it against an `httptest` server and no test performs
network I/O.

`GUARDIAN_NETWORK_LIST_URL` overrides the constant and accepts a local path as
well as a URL. That is not a testing convenience: the chain's devnet drives
`guardianctl config init --non-interactive` (`devnet/guardians.sh:171`,
`devnet/docker/init-guardians.sh:97`), and without an override every `dev-up` and
every e2e run would depend on reaching GitHub. Those scripts point the override at
the chain checkout's own `networks.json`, which is strictly better than today's
arrangement: the devnet then exercises the real selection path, offline, against
the file the chain owns rather than relying on this daemon's compiled literals
happening to match it.

### When the fetch fails

The fetch gets a short timeout (5 seconds) and no retry. What follows depends on
whether anyone is there to answer.

**Interactively**, `config init` reports the failure in one line and falls through
to the manual path, where the operator types the three fields or accepts the
compiled defaults. Setup must complete on a host that cannot reach the internet.

**Unattended**, it is a hard error. This is the one place a silent fallback would
be actively harmful: the design below makes "no network named" mean "the published
default", so quietly substituting the compiled `timeflare-test` literals would
reintroduce exactly the real-looking-wrong-value failure this plan exists to
close. A setup command that fails loudly is re-runnable; a guardian that bonded
against the wrong chain id is not.

### Validation is shallow, on purpose

The parse checks only what the guardian reads: `default` is a string naming an
entry that exists, `networks` is non-empty, and every entry carries `id`,
`label`, `chainId`, `local` and array-valued `endpoints.rpc` and
`endpoints.grpc`. Unknown fields are ignored rather than rejected. A list
carrying a field this build does not understand is a newer chain talking to an
older `guardianctl`, which is the normal case over time and not an error.

`local` is required and must be a boolean, because the transport decision derives
from it — see "Reaching a network that is not loopback". An entry that omits it is
not usable, which is why it sits with the required fields rather than defaulting.

`endpoints.rest` is parsed but unused: the guardian speaks native gRPC and
CometBFT RPC, and has no REST client. It is read so the type mirrors the
published shape rather than a guardian-shaped subset of it.

### Lists become scalars, for now

The registry's endpoints are lists so that one unreachable host is not an
unreachable network. The guardian's `rpc_endpoint` and `grpc_endpoint` are single
strings, so selection takes the first element of each and the remainder are
discarded with no record. That is a real narrowing and it is stated here rather
than glossed: the operator gets one host out of however many were published.

Widening the fields into lists with rotation on `ErrUnavailable` belongs to
[PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
Phase 6, whose open question 4 must be settled first. When it lands, selection
writes the whole list and this narrowing disappears; nothing in this plan needs
revisiting to allow that.

### An empty `grpc` list disqualifies a network

`NETWORKS.md` is explicit that `"grpc": []` is a statement rather than an
omission — plenty of deployments expose 1317 and not 9090 — and that a daemon
needing gRPC should learn at configuration time that it cannot run against such
a network, not at its first dial.

So a network with no gRPC endpoint is *listed but not selectable*, shown with the
reason attached rather than filtered out silently. An operator who cannot find
the network they were told to join needs to know it was seen and rejected, and
why.

### The wizard step

Network selection becomes **Step 1**, ahead of the signing key: it is the
question with the widest consequences and the one an operator arrives already
knowing the answer to. The existing steps renumber to 2–5
(`collectSigningKeyName`, `collectKeyringPassphrase`, `collectEncryptionKey`,
`promptForDashboardPasswordDuringInit` and their headings).

The step lists each network as one unit — label, chain id, and the endpoints that
come with it — marks the registry's `default`, and offers a final **custom**
option that drops through to typing the three fields by hand. That option is the
guardian's own and never the chain's: a private node, or a network the published
list does not carry, must always remain reachable.

A hand-typed endpoint gets no `grpc_tls` inference, because nothing in the
configuration says whether that host terminates TLS. It keeps the default, and the
transport-security plan's warning phase is what tells an operator who typed a
remote host that their link is in clear — which is why that phase stays with that
plan rather than coming here.

It follows the shape the wizard already uses for the encryption key
(`collectEncryptionKey`): a `collectNetwork` function that *returns* the values
and writes nothing, with `applyInitSettings` remaining the single place the
configuration is touched. `initSettings` gains `network`, `chainID`,
`rpcEndpoint`, `grpcEndpoint` and `grpcTLS`; `applyInitSettings` writes the
endpoints when non-empty, so an operator who declines selection keeps the compiled
defaults.

### The configuration records which network was chosen

A `network` key in the Network group holds the selected entry's `id` — `custom`
for a hand-typed endpoint, matching the wizard's own option. Without it the
configuration keeps three values whose origin is unrecoverable: an operator
reading `chain_id: timeflare-testnet-1` cannot tell whether they selected that
network or typed it, and neither can `config doctor`.

It earns its place immediately rather than waiting for a consumer. `config doctor`
gains a drift check: when `network` names a published entry and the endpoints or
chain id no longer match what that entry carries, it reports the difference and
names both values. That covers the case this design otherwise leaves silent — a
published endpoint moving after setup — turning it from something an operator finds
out by failing into something a routine check reports. `custom` and an unknown id
are both reported as "not checked" rather than as drift, since neither has anything
to compare against.

### Unattended runs

`--network <id>` selects by id and skips the prompt. It resolves against the
fetched list, and an id that is absent is a hard error naming the ids that were
available — a script naming a network which no longer exists must fail rather than
quietly land somewhere else.

`--non-interactive` **without** `--network` still fetches, and takes the entry the
list names as `default` (ruled by the owner, 4 August 2026). So the published
registry decides what an unnamed network means, rather than whatever literals the
binary was compiled with — which is the point of consulting it at all, and it
closes finding 22 on the scripted path as well as the interactive one.

The compiled defaults in `DefaultConfig()` serve a narrower purpose: they are what
a configuration carries before anything sets it, and the starting point for the
interactive manual path.

### Reaching a network that is not loopback

A selected network has to be dialable, and today only a loopback one is.
`internal/chain/client.go:41` and `internal/cli/key.go:440` both dial with
`insecure.NewCredentials()`, which is not lax verification: it is cleartext h2c,
and no TLS handshake is ever attempted. A registry entry for a public network
carries `grpc.<host>:443`, where the server waits for a ClientHello and receives
the HTTP/2 connection preface instead, so the connection never establishes — and
because `grpc.NewClient` dials lazily, that surfaces at the first query rather
than at startup, after `guardiand start` has reported itself healthy.

So this plan carries **Phase 1 of
[PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)**:
the `grpc_tls`, `grpc_tls_ca_file` and `grpc_tls_insecure_skip_verify` keys, with
credentials selected from them by **one helper shared** by `NewClient` and the
`key.go` call site — that plan's open question 4, answered yes, because two places
deciding transport security is precisely the duplication that leaves one of them
behind. Its design is adopted unchanged rather than restated; the reason it lands
here is that network selection is what populates it, and selection producing a
configuration the daemon cannot dial is not worth shipping.

**Selection sets `grpc_tls` from `!local`.** A loopback-scoped network gets
plaintext — the devnet, and the only place the registry permits cleartext at all;
anything else gets TLS against the system root pool. The operator therefore never
has to know the key exists, and the explicit boolean that plan wants — rather than
a scheme parsed out of the endpoint, its open question 3 — is what a selection step
fills in.

The derivation is sound because `local` is not merely correlated with transport:
`verify-networks` requires a `local` entry's URLs to be loopback and a non-local
entry's to be `https`, so the registry cannot publish a network where the two
diverge. Reading `local` here is reading the rule, not guessing from it.

The inference is confined to selection. A configuration nobody selected into keeps
the plaintext default, and `grpc_tls` remains an ordinary key an operator can set
either way — a private node on a LAN serving plaintext gRPC stays reachable through
the custom path.

The default stays plaintext for a configuration nobody selected into, so every
colocated deployment and the devnet are untouched. That answers that plan's open
question 1 for the case that matters: the safer setting arrives with the network
that needs it, instead of being a default that breaks every local setup.

The RPC leg needs nothing here. `internal/guardian/events.go:65` uses
`rpchttp.New`, which honours the `https://` and `wss://` URLs a non-local entry
publishes, verifying against the system pool. A private CA reaching the event
monitor stays that plan's Phase 3.

### What the daemon still needs to check

Selecting from a published list removes the typo. It does not verify that the
node on the other end is the chain it claims to be, and it cannot: whoever serves
the list chooses the endpoints a guardian dials.

The bound on that is the chain-id assertion at startup —
[PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md)
Phase 4 — which compares the configured `chain_id` against what the node
reports. This plan makes that assertion more valuable rather than less: a
`chain_id` taken from the registry is one an operator has no reason to doubt, so
the check that it matches the node becomes the only thing standing between a
substituted endpoint and a guardian signing for the wrong chain.

### The economics stay out of the registry

The Network group is not the only place this daemon hardcodes a value with an
authority elsewhere. The Economics group carries four, and they divide cleanly
once their provenance is checked:

| Key | Current default | Authority |
|---|---|---|
| `denom` | `uveil` | `x/secrets/types.DefaultDenom` (`constants.go:482`) |
| `gas_price` | `0.1uveil` | consensus floor `MinGasPriceUveilNum/Den` = 1/10 (`constants.go:362-372`) |
| `stake_amount` | `10000000000uveil` | none — no such value exists on the chain |
| `fee_buffer_percent` | `1` | none — this daemon's own headroom |

The first two are **protocol constants, already exported by the module this
repository imports**, and `internal/chain/signer.go:68-69` already reads the gas
floor from it at runtime. Only the compiled defaults are hand-copies.

`stake_amount` cannot be read from anywhere, and the reason is worth recording so
it is not looked for again. The float deposit is **optional and may be zero**
(`chain/docs/spec.md:261`, `:286`); `MsgGuardianRegister.ValidateBasic` checks only
that the coin is valid (`message_register_guardian.go:40-42`); and the chain has no
parameter system at all to hold a default — `spec.md:629` states there is no
`Params` state and no `MsgUpdateParams`, because an economic constant that could
move beneath a year-long secret is a product defect. So there is no minimum float
and no recommended one.

What the module does export is the bond formula's inputs — `RatePerGuardianBlock`,
`InitialBondK` (4.00), `BumpScale`, `MinBump`/`MaxBump` and
`MaxActiveBondsPerGuardian` (100). A bond is `rate × distance × bump × k`, and
`distance` is the individual secret's block span, so no float figure follows from
them without assuming a secret duration. Deriving the default would mean inventing
that assumption and presenting it as authority, which is worse than a round number
an operator overrides with `--stake-amount`. It stays as it is.

**The entry fee is a different matter.** `internal/cli/register.go:56` states "the
1,000 VEIL entry fee" in flag help text, which is `secretstypes.EntryFeeAmount`
(`constants.go:222-226`) written out by hand — a protocol constant copied into
operator-facing copy, where it will be wrong the moment the chain retunes it in an
upgrade. That help text reads the constant instead.

They therefore do not belong in `networks.json`. That file is scoped to deployment
fact and gated by `make verify-networks`; a consensus value restated there would
be a *second* copy of something the wire contract already publishes, in a place
with no equivalent check to catch the drift — reintroducing the exact problem the
registry exists to remove. Adding them would make the registry authoritative for
something it is not.

So `DefaultConfig()` derives them from `secretstypes` instead of literals:
`denom` from `DefaultDenom`, and `gas_price` from the floor already computed in
`signer.go`, factored out so both callers read one expression. This lands here
rather than in a plan of its own because it is the same concern the plan opens
with — a network value hardcoded where an authority exists — and it is two lines;
[PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
continues to own `gas_price` *validation*, which is a different gap.

`stake_amount` and `fee_buffer_percent` are genuine operator preference and stay
as they are. `block_time` (6s, matching the devnet's `TIMEFLARE_BLOCK_TIME`
default) is per-deployment rather than consensus and is the only defensible
candidate for a future registry field — but its own comment scopes it to display
maths and derived defaults, so a wrong value is cosmetic. Left out until a network
runs at a materially different rate.

## Phases

1. **`internal/config/networks.go`** — the `NetworkList` and `RegistryNetwork`
   types, `NetworkListURL`, `FetchNetworkList(ctx, url)`, shallow validation,
   and lookup by id plus the default. New file in the existing `config` package;
   no new package, binary or build target.
2. **`internal/config/networks_test.go`** — validation table (well-formed;
   `default` naming nothing; empty `networks`; missing fields; unknown fields
   accepted), and fetch against `httptest` for success, non-200, malformed body
   and timeout.
3. **The gRPC TLS keys** — `grpc_tls`, `grpc_tls_ca_file` and
   `grpc_tls_insecure_skip_verify` on `Config`, and one credential-construction
   helper used by both `internal/chain/client.go:41` and
   `internal/cli/key.go:440`. Adopted from the transport-security plan's Phase 1;
   its remaining phases are unaffected. Ahead of the wizard phase, so selection
   has a field to write.
4. **`collectNetwork` in `internal/cli/config_init.go`** — the Step 1 prompt,
   the `--network` flag in `initFlags`/`readInitFlags`, the unselectable-network
   presentation, the custom fallback, and the renumbering of Steps 2–5.
   `initSettings` and `applyInitSettings` gain the five fields, with `grpc_tls`
   from `!local` and `network` from the entry's `id`.
5. **The `network` key and its drift check** — the field on `Config` in the
   Network group, and the `config doctor` comparison of `chain_id` and both
   endpoints against the named entry, reporting `custom` and unknown ids as not
   checked.
6. **`internal/cli/config_init_test.go`** — selection writes all five fields;
   a `local: false` entry sets `grpc_tls` and a `local: true` entry leaves it off;
   `--network` with an unknown id fails naming the available ids; a failed fetch
   completes interactively with the defaults intact and fails unattended;
   `--non-interactive` with no `--network` lands on the list's `default`. Doctor's
   drift check gets a matching case per branch.
7. **`DefaultConfig()` derives `denom` and `gas_price` from `secretstypes`** —
   the gas floor expression factored out of `internal/chain/signer.go:68-69` so
   both callers read one definition — and `internal/cli/register.go:56` reads
   `EntryFeeAmount` rather than stating 1,000 VEIL in prose.
8. **The chain's devnet scripts** — `devnet/guardians.sh` and
   `devnet/docker/init-guardians.sh` set `GUARDIAN_NETWORK_LIST_URL` to the chain
   checkout's own `networks.json`, so `dev-up` and the e2e suites exercise
   selection without reaching GitHub. A change in the chain repository, landing
   after the guardian release that understands the variable, and the only
   chain-side work this plan still implies.
9. **Docs and the transport plan** — the operator setup guide gains the selection
   step; `CLAUDE.md` gains a line stating that the network registry is read at
   `config init` only and that the daemon's source of truth remains its
   configuration file; and
   [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)
   is rewritten to open at its warning phase, with its open questions 1, 3 and 4
   settled and its component list reduced to what it still owns.

## What this plan does not solve

- **It does not complete the transport posture.** TLS with the system pool is
  where this stops. No certificate pinning, no mTLS, no warning when a hand-typed
  non-loopback endpoint is left plaintext, no private CA reaching the event
  monitor, and no fix for the host-blind port-collision check — all still
  [PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md](PENDING_GUARDIAN_TRANSPORT_SECURITY_PLAN.md)'s,
  which this plan reduces rather than replaces.
- **TLS does not make the node trustworthy.** An authenticated channel to a
  hostile node is still a channel to a hostile node. The bounds on that are the
  gas ceiling and the chain-id assertion in
  [PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md](PRIORITY_GUARDIAN_TX_PATH_RESILIENCE_PLAN.md),
  and both matter more once endpoints arrive from a published list rather than
  from the operator.
- **It does not make `block_time` per-network.** It stays at 6s in
  `DefaultConfig()`, which is the devnet's rate. A testnet running materially
  faster or slower would need it set by hand, affecting display maths and derived
  defaults only.
- **It does not make `AddressPrefix` registry-driven.** The registry publishes
  `addressPrefix`, but `internal/config/config.go:15` is a compile-time constant
  behind every address validation path in the daemon. The chain already gates it
  against `app.go`, and it is project-wide rather than per-network, so it stays
  as it is.
- **It does not follow a moved endpoint automatically.** Values are copied into
  the configuration at setup and never re-read, so a published endpoint that moves
  leaves an existing guardian on the old one until an operator acts — `config
  doctor` reports the drift, and applying it is a decision rather than something
  that happens underneath a running daemon. Re-reading at start would let whoever
  serves the list redirect a guardian between restarts, which is a worse trade than
  a stale endpoint an operator can see in their own configuration file.

## Open questions

None outstanding.
