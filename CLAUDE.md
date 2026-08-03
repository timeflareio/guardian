# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project Overview

**timeflare guardian** is `guardiand`, the daemon third-party guardians run. It
accepts secret assignments from the chain, holds one Shamir share of each
secret's key, and reveals that share when the time-lock opens — earning a reward
for doing so, and losing bond for revealing early or not at all.

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

## 📋 Specification Authority

`docs/spec.md` **in the chain repository** is the single source of truth for
protocol behaviour, guardian eligibility and selection, bonding and slashing
amounts, and secret lifecycle timing. Link it at a pinned tag; never copy it
here.

If the spec is unclear or silent on something this daemon needs: **stop** and ask
the owner to clarify and update it. Do not infer protocol behaviour from this
daemon's existing code — the code may be the thing that is wrong.

## 🚨 Plan-First Workflow (mandatory — everything)

All work is executed from an approved plan in `docs/planning/`. Code, docs,
dependency changes: every change traces to a plan the owner has approved.
Discussion is not approval — answering a question or receiving a favourable reply
is never licence to edit. Propose, wait for the ruling, fold it into a plan, then
execute. The only exception is a change the owner explicitly requests in the
moment, and even then the scope is exactly what was asked.

The rules for authoring and refining plans are in `docs/planning/README.md`.

## Important Instructions for Claude

- Do what has been asked; nothing more, nothing less
- NEVER create files unless explicitly asked to implement or code a solution
- When asked to "elaborate", "explain", or give "feedback", give verbal
  explanations only
- ALWAYS prefer editing existing files over creating new ones
- **🚨 CRITICAL: When asked to create a "plan", ONLY create the plan document —
  DO NOT start implementing**
- **Always wait for explicit approval** before proceeding from planning to
  implementation
- **🚨 CRITICAL: Keep the architecture minimal.** Never introduce a new
  component without arguing the case and getting explicit confirmation first.
  Default to extending what exists.
- NEVER create code in production code spaces purely for the purpose of tests
- **Documentation Language**: ALL documentation must use British English
- **Spelling Standard**: use `-ise` endings (organise, realise), `-our` endings
  (behaviour), `-sation` endings (organisation)
- **🚨 VEIL is a token, never money.** Do not use "money", "cash", "funds",
  "payment" or any currency framing for VEIL — in code, comments, documentation,
  plans, commit messages, UI copy or conversation. Say "token", "VEIL", "uveil",
  "balance", "amount", "fee", "cost", "bond", "reward" or "rebate". This is not a
  style preference: describing a token as money makes a regulatory claim the
  project does not make.
- **🚨 NEVER name the owner.** No personal name appears anywhere in the
  repository — not in code, comments, documentation, plans, commit messages or
  test fixtures. Decisions are attributed to **"the owner"**, never to a person.
  This covers every form: given name, surname, handle, email address, and machine
  paths that embed a username.
