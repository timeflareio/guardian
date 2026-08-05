# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

**It is not the whole picture.** The project-wide rules — the working agreement,
the writing conventions (British English, VEIL is a token, never name the owner),
the plan-first mandate, specification authority, and how a change crosses a
repository boundary — are stated once in the workspace root `CLAUDE.md`, at
`~/dev/timeflareio/CLAUDE.md`, which loads alongside this file. Read it if you
are in a checkout that cannot see it.

## Project Overview

**timeflare guardian** is `guardiand`, the daemon third-party guardians run. It
accepts secret assignments from the chain, holds one Shamir share of each
secret's key, and reveals that share when the time-lock opens — earning a reward
for doing so, and losing bond for revealing early or not at all.

It ships as **two binaries**. `guardiand` runs the service — `start`, `health`,
`version` — and `guardianctl` is the operator tool: `config`, `wallet`, `key`,
`register`, `update`, `rotate-key`, `status`. The division is a custody decision,
not packaging: the daemon holds the epoch keyring and is the only component with
network-facing surface, so it must carry no code that can mint, export or rewrite
key material. `make verify` checks that on the linked binary
(`verify-daemon-symbols`) — a key verb added to the daemon's root fails the
build. Both roots live in `internal/cli`; the linker's reachability is what keeps
them apart, which is why the check is on symbols rather than imports.

It holds long-lived key material. `docs/guides/GUARDIAN_KEY_CUSTODY.md` is the
authority on how, and any change touching `internal/custody/` should be read
against it first.

## What this repository depends on

Two pinned public modules, and nothing else of ours:

| Dependency | Import path | What it is |
|---|---|---|
| wire contract | `github.com/timeflareio/chain/x/secrets/types` | message types, constants, `ValidateBasic` |
| primitives | `github.com/timeflareio/crypto/go` | HMAC, X25519 + ChaCha20-Poly1305 |

Note the second: the **module** is `github.com/timeflareio/crypto`, and `go/` is
a package directory inside it — so `go.mod` requires the unsuffixed path while
import statements carry `/go`. The package is named `crypto`, so imports use an
explicit alias: `crypto "github.com/timeflareio/crypto/go"`.

## 🚨 Never import chain internals

`x/secrets/types` is the **only** part of the chain this daemon may know about.
Not the keeper, not the module wiring, not `app`, not `cmd`. That separation is
the entire reason the wire contract is its own module.

Inside the monorepo this was obvious by proximity. It no longer is: the chain is
a public repository and a `require` is one command away, so nothing stops a
plausible-looking import except `make verify-boundaries`. Treat a failure there
as a design error, not a lint nuisance.

## 🚨 The carried pin block must mirror the chain's exactly

**Go honours `replace` directives only in the main module being built.** A
dependency's replaces are ignored entirely. So the chain's carried pins do
nothing for this binary — `guardiand` must carry an identical block itself.

If the two blocks drift, `guardiand` and `timeflared` build against **different
cosmos-sdk versions** and can diverge in wire behaviour. Nothing in this
repository's tests would catch that: both sides compile, both sides pass, and the
disagreement only appears against a live chain.

`make verify-pins` fetches the chain's `go.mod` at the pinned wire-contract
version and diffs the actual replace directives. It runs as part of `make
verify`. When it fails:

- **Do not** simply edit this repository's block to match and move on — ask why
  they diverged. If the chain moved, this module follows *and* may need its
  required wire-contract version bumping.
- A cosmos-sdk bump is a **T2 change** and a two-repo train: the chain moves
  first, then this module. Never the other way round.

## The chain vectors travel with the wire contract

Two **chain-owned** vector files — `wallet_derivation` (key path layout) and
`client_conventions` (mnemonic handling) — are asserted by this daemon's tests
and live in `x/secrets/types/testdata/vectors/`, inside the module `go.mod`
already requires. `chainVectorsDir` in each test locates the module and reads
them from there.

That means the version in `go.mod` is the only pin, and the Go checksum database
is the integrity check. There is nothing to sync, nothing to verify against a
manifest, and no copy in this repository to drift or be hand-edited.

The primitive vectors are a separate corpus, owned by `timeflareio/crypto`. This
daemon asserts none of them: it consumes the primitives as a module and lets that
module's own suites prove them.

## The network registry is read at `config init` and nowhere else

`guardianctl config init` reads the network list the chain publishes
(`chain/networks.json`, documented in `chain/docs/guides/NETWORKS.md`) and writes
the chosen entry's `chain_id`, `rpc_endpoint`, `grpc_endpoint` and `grpc_tls` into
the configuration. **The configuration is the daemon's only source thereafter**,
so nothing at runtime depends on that list being reachable and a guardian already
configured never consults it.

Two consequences worth keeping:

- **Do not add a second reader.** Re-reading at startup would let whoever serves
  the list redirect a running guardian between restarts. `config doctor` reports
  drift against the `network` key instead, and applying it stays an operator's
  decision.
- **`grpc_tls` follows the entry's `local`**, not a guess about the hostname. The
  chain's `verify-networks` requires a local entry's URLs to be loopback and a
  non-local entry's to be `https`, so locality *is* the transport rule rather than
  a proxy for it. Cleartext is permitted for loopback and nowhere else.

`GUARDIAN_NETWORK_LIST_URL` overrides the source and takes a path as readily as a
URL. The test suite and the chain's devnet both point it at a local file, which is
why neither depends on reaching GitHub.

## Essential Commands

- `make test` — all unit tests
- `make verify` — format, lint, imports, vet, **module boundary, output
  boundary, daemon symbols, carried pins, vendored vectors**
- `make clean` — apply formatting and lint fixes
- `make build` — build and install `guardiand` and `guardianctl`
- `make go-govulncheck` — vulnerability scan (advisory; accepted findings and
  their reachability reasoning are in `.govulncheck-accepted`)

## Specific to this repository

- **The spec is not here.** `docs/spec.md` in the chain repository governs
  guardian eligibility and selection, bonding and slashing amounts, and secret
  lifecycle timing. Link it at a pinned tag; never copy it here. The rules for
  consulting it are in the workspace root `CLAUDE.md` — including the one that
  bites hardest for a consumer: never infer protocol behaviour from this daemon's
  existing code, because the code may be what is wrong.
- Plans live in `docs/planning/`, per its `README.md`.
