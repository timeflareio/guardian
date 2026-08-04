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

It holds long-lived key material. `docs/guides/GUARDIAN_KEY_CUSTODY.md` is the
authority on how, and any change touching `custody/` should be read against it
first.

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

## The vendored chain vectors

`testdata/vectors/` holds a pinned copy of two **chain-owned** vector files —
`wallet_derivation` (key path layout) and `client_conventions` (mnemonic
handling) — which this daemon's tests assert. The chain owns them; the pin is a
chain tag in `testdata/vectors/CHAIN_VECTORS_VERSION`.

- `make vectors-verify` — checks the copy against the pinned chain tag (part of
  `verify`)
- `make vectors-sync CHAIN_VECTORS_VERSION=vX.Y.Z` — refresh from a chain tag

Never hand-edit them. A local edit means this daemon is asserting conventions the
chain does not implement, which is exactly the drift the corpus exists to catch.

The five **primitive** vectors are a separate corpus, owned by
`timeflareio/crypto`. This daemon asserts none of them: it consumes the
primitives as a module and lets that module's own suites prove them.

## Essential Commands

- `make test` — all unit tests
- `make verify` — format, lint, imports, vet, **boundaries, carried pins,
  vendored vectors**
- `make clean` — apply formatting and lint fixes
- `make build` — build and install `guardiand`
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
