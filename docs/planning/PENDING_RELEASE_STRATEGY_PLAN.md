# Release Strategy — `timeflareio/guardian` — Plan

*Settles how this repository versions and releases. Its distinguishing feature is
that the artefacts are run by **third parties** on machines holding long-lived key
material, which changes what a release has to guarantee.*

> **Status: refining** — created August 2026 with the phase-3 lift. §5 is
> unresolved; not executable until those questions are ruled and folded in.
> **Priority**: P2 — the migration's `v0.0.x` tags serve for now, but this must
> land before any third party is asked to run `guardiand`.
> **Origin**: multi-repo migration plan (in the monorepo), which delegated
> release strategy to a per-repo plan.
> **Components**: `.github/workflows/release.yml`, `Makefile`, and the
> compatibility contract with `timeflareio/chain`.

## 1. What this repository publishes

| Artefact | Consumed by |
|---|---|
| `guardiand` binaries — darwin/linux × arm64/amd64 | guardian operators |
| `guardianctl` binaries — same matrix | guardian operators |
| one `checksums.txt` covering both | guardian operators |
| distroless image `ghcr.io/timeflareio/guardiand:vX.Y.Z`, shipping both binaries | container deployments, the chain's compose devnet |

**Two binaries, one version.** `guardiand` runs the service; `guardianctl` holds
configuration, the signing key, share-key backup and restore, registration and
rotation — every verb that can write or export key material, which is exactly why
the daemon carries none of them
([PENDING_DAEMON_SURFACE_SEPARATION_PLAN.md](PENDING_DAEMON_SURFACE_SEPARATION_PLAN.md)
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

So a release must state, and the workflow does state:

- the `x/secrets/types` version it was built against — the wire contract
- the `crypto` version — the primitives
- the chain vector corpus tag its conventions were asserted against
- that `verify-pins` passed, i.e. the carried cosmos-sdk block matched the
  chain's at that wire-contract version

Those four lines are the release's real content; the version number is a label
for them.

## 3. Release mechanics

- **Trigger**: pushing a `vX.Y.Z` tag. Nothing releases on merge.
- **Preconditions**: the 0.x version guard, then `make verify` (which includes
  `verify-boundaries` and `verify-pins`) and `make test`.
- **Binaries** cross-compiled with `-trimpath` and `CGO_ENABLED=0`, stamped with
  the tag and commit, plus a `checksums.txt`.
- **Image** built multi-arch from the repository's own `Dockerfile` and pushed to
  GHCR.

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
   outcome available in this system. *Recommendation*: treat this as blocking
   before any third-party operator, and solve it jointly with the chain's
   identical problem in a project-wide supply-chain plan rather than here. Note
   that the guardian's exposure is worse than the chain's: a validator running a
   bad binary damages itself, whereas a guardian running one damages the users
   whose secrets it holds.

2. **Does the image get a `latest` tag, or a rolling minor tag?** Convenient for
   operators, dangerous for a daemon holding key material — an unattended pull
   could silently change the binary custodying shares. *Recommendation*: publish
   exact version tags only. Make operators choose their version deliberately, and
   say so in the release notes.

3. **How are operators told an upgrade is mandatory?** A wire-contract change
   makes older guardians non-functional, and a guardian that silently stops
   accepting secrets loses reward and may be slashed for missed reveals.
   *Recommendation*: reserve MINOR (while 0.x) for "must upgrade to keep working
   against the current chain", and state the required chain version range in
   every release note. A version number alone cannot convey urgency, so the notes
   have to.

4. **Should a release verify against a live chain, not just unit tests?** The
   chain's e2e suites exercise `guardiand` end to end, but they live in the chain
   repository and arrive with phase 2b. *Recommendation*: once phase 2b lands,
   have this repository's release workflow trigger the chain's e2e suite against
   the candidate build via `repository_dispatch`, and refuse to publish if it
   fails. Unit tests alone cannot show that this daemon still completes a secret
   lifecycle.

## 6. What this plan does not solve

- **Key custody and operator security** — `docs/guides/GUARDIAN_KEY_CUSTODY.md`,
  not release mechanics.
- **Bond and slashing economics** — the chain's spec.
- **How operators discover releases.** There is no announcement channel, which
  matters once §5.3's "mandatory upgrade" case is real.
