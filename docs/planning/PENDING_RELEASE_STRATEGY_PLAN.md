# Release Strategy — `timeflareio/guardian` — Plan

| | |
|---|---|
| **Priority** | P2 |
| **Status** | refining |
| **Created** | August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian, chain |
| **Depends on** | — |
| **Related** | [DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md); `timeflareio/chain` — `PENDING_RELEASE_STRATEGY_PLAN.md`; `timeflareio/chain` — `PENDING_RELEASE_ENGINEERING_PLAN.md` |
| **Open questions** | 5 (§5) |

*Settles how this repository versions and releases. Its distinguishing feature is
that the artefacts are run by **third parties** on machines holding long-lived key
material, which changes what a release has to guarantee.*

**Status notes**: the release mechanics exist and have shipped `v0.0.1` to
`v0.0.5`; §1–§4 describe them as they stand. What remains is §5 — five
questions that must be ruled before a third party is asked to run these
binaries. Not executable until they are ruled and folded in.
**Why this priority**: the `v0.0.x` tags serve for now, but this must
land before any third party is asked to run `guardiand`.
**Origin**: multi-repo migration plan (monorepo history; not present in any
repository), which delegated release strategy to a per-repo plan.
**Components**: `.github/workflows/release.yml`, `Makefile`, an operator guide
under `docs/guides/` (new, if §5.5 is ruled that way), and the compatibility
contract with `timeflareio/chain` (`COMPATIBILITY.md` there).

## 1. What this repository publishes

Current state, as `.github/workflows/release.yml` produces it:

| Artefact | Consumed by |
|---|---|
| `guardiand` binaries — darwin/linux × arm64/amd64 | guardian operators; the chain's devnet |
| `guardianctl` binaries — same matrix | guardian operators; the chain's devnet |
| one checksums file covering both | guardian operators |
| distroless image `ghcr.io/timeflareio/guardiand:vX.Y.Z`, shipping both binaries | container deployments, the chain's compose devnet |

**Two binaries, one version.** `guardiand` runs the service; `guardianctl` holds
configuration, the signing key, share-key backup and restore, registration and
rotation — every verb that can write or export key material, which is exactly why
the daemon carries none of them
([DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md)
§4 Phase 5). They are released from the same tag and must be installed at the same
version: `guardianctl` writes the configuration and key layout the daemon reads,
so a mismatched pair is the same class of silent divergence as a stale
cosmos-sdk pin. The release notes say so, and one checksums file covering both
makes the pairing awkward to break by accident.

**No Go module.** Nothing imports this repository, so the tag exists to identify
a build, not to serve a library.

Binaries are released rather than left to `go install`, and this is forced rather
than chosen: the module carries `replace` directives for the cosmos-sdk pin set,
and `go install mod@version` refuses any module whose go.mod has replaces. Until
those pins' exit conditions are met, release assets are the only route.

## 2. What a version number means

Unusually for a released binary, the compatibility question here is not mainly
"what changed in this repository" but **"which chain does this build speak to"**.
A guardian that mirrors a stale cosmos-sdk pin, or was built against an older
wire contract, can fail against a live chain while passing every test here.

So a release states, in its generated notes:

- the `x/secrets/types` version it was built against — the wire contract, which
  also carries the two chain-owned vector files this daemon's tests assert, so
  there is no separate corpus version to report
- the `crypto` version — the primitives
- that `verify-pins` passed, i.e. the carried cosmos-sdk block matched the
  chain's at that wire-contract version

Those three lines are the release's real content; the version number is a label
for them.

## 3. Release mechanics

Current state:

- **Trigger**: pushing a `vX.Y.Z` tag. Nothing releases on merge.
- **Preconditions**: the 0.x version guard, then `make verify` (which includes
  `verify-boundaries`, `verify-daemon-symbols` and `verify-pins`) and
  `make test`.
- **Binaries** cross-compiled with `-trimpath` and `CGO_ENABLED=0`. Nothing
  stamps a version: the toolchain derives it from the tag in the checkout, and
  the workflow fails the release if either binary does not report the tag it
  was built from.
- **Image** built multi-arch from the repository's own `Dockerfile` and pushed to
  GHCR under the exact version tag only.

## 4. Independence from the chain

This repository versions independently, but **not freely**: a chain release that
changes the wire contract or the pin set obliges a release here, because
`verify-pins` will otherwise fail and operators would be running a daemon that
disagrees with the network. The dependency is one-way — the chain never waits for
the guardian — so in practice guardian releases trail chain releases.

`COMPATIBILITY.md` in the chain repository is the join table. This repository's
obligation is to make its row truthful, which the generated release notes do.

## 5. Open questions

1. **Are the binaries signed, and how does an operator verify one?** Checksums
   protect against corruption, not substitution, and a substituted `guardiand` on
   an operator's machine means stolen shares and slashed bonds — the worst
   outcome available in this system. The guardian's exposure is worse than the
   chain's: a validator running a bad binary damages itself, whereas a guardian
   running one damages the users whose secrets it holds. This question was to be
   solved jointly with the chain's identical one "in a project-wide supply-chain
   plan" — and **no such plan exists** in any repository. The nearest is
   `timeflareio/chain` — `PENDING_RELEASE_ENGINEERING_PLAN.md`, which weighs
   cosign against a project GPG key but was written when `guardiand` was built
   and released from the chain's repository, and still says it "is not released
   at all".
   *Recommendation*: treat signing as blocking before any third-party operator,
   and give it one owner — fold the guardian's artefacts into the chain's
   release-engineering plan once that plan is corrected for the split, rather
   than open a third document.

2. **Does the image get a `latest` tag, or a rolling minor tag?** Convenient for
   operators, dangerous for a daemon holding key material — an unattended pull
   could silently change the binary custodying shares. The workflow publishes
   the exact version tag and nothing else; that is how it was written, not a
   recorded ruling. *Recommendation*: publish exact version tags only. Make
   operators choose their version deliberately, and say so in the release notes.

3. **How are operators told an upgrade is mandatory?** A wire-contract change
   makes older guardians non-functional, and a guardian that silently stops
   accepting secrets loses reward and may be slashed for missed reveals.
   *Recommendation*: reserve MINOR (while 0.x) for "must upgrade to keep working
   against the current chain", and state the required chain version range in
   every release note. A version number alone cannot convey urgency, so the notes
   have to.

4. **Should a release verify against a live chain, not just unit tests?** The
   chain's e2e suites exercise `guardiand` and `guardianctl` end to end. They
   live in the chain repository (`timeflareio/chain` — `devnet/`), run against
   the guardian release pinned in `devnet/versions.env`, and each
   `COMPATIBILITY.md` row is added only after they pass — so today a guardian
   release is exercised against a live chain *after* it is published, when the
   chain moves its pin, and never before. *Recommendation*: have this
   repository's release workflow trigger the chain's e2e suite against the
   candidate build via `repository_dispatch`, and refuse to publish if it
   fails. Unit tests alone cannot show that this daemon still completes a secret
   lifecycle.

5. **Where is the operator install and run guide, and which plan writes it?**
   [DONE_DAEMON_SURFACE_SEPARATION_PLAN.md](done/DONE_DAEMON_SURFACE_SEPARATION_PLAN.md)
   records that this repository has no install or run guide, that the plan
   changed the interface such a guide would describe (no start prompt, new
   flags, seven verbs moved to a second binary), and that the guide "is owed
   before the release … §5.1 gates, and wants its own plan". No such plan
   exists, and `docs/guides/` holds only the key-custody guide.
   *Recommendation*: make the guide a deliverable of this plan, gated with
   question 1 — both are preconditions of the same event, the first release a
   third party is asked to run — rather than a separate plan.

## 6. What this plan does not solve

- **Key custody and operator security** — `docs/guides/GUARDIAN_KEY_CUSTODY.md`,
  not release mechanics.
- **Bond and slashing economics** — the chain's spec.
- **How operators discover releases.** There is no announcement channel, which
  matters once §5.3's "mandatory upgrade" case is real.
