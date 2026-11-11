# Dashboard Guardian Actions — Plan

| | |
|---|---|
| **Priority** | P2 |
| **Status** | refining |
| **Created** | 1 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian, chain |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md); `timeflareio/chain` — `DONE_DASHBOARD_AUTHENTICATION_PLAN.md`; `timeflareio/chain` — `DONE_GUARDIAN_DASHBOARD_PLAN.md` |
| **Open questions** | 6 (§8) |

*Guardian operations from the operator dashboard — availability updates, float
deposits, the accepting-secrets toggle, and withdrawal of the unlocked float —
mirroring exactly what `guardianctl` executes from the CLI, plus one dashboard
presentation change (slightly lighter text). This is the state-changing
successor the dashboard plan deferred: the first slice of "runbook mode".*

**Status notes**: proposed 1 August 2026. Not executable: §8 Q1 — the design of
the dashboard's login — gates every state-changing route, and nothing about it
has been designed or decided.

**Why this priority**: operator UX.

**Origin**: owner request, 1 August 2026 ("perform operations as a guardian
from the dashboard … mirror exactly we would run from the guardian cli
tooling … availability, withdrawing [the float], any other operation the
guardian can action"), discharging the handoff recorded in
`timeflareio/chain` — `DONE_GUARDIAN_DASHBOARD_PLAN.md`, which parked
state-changing routes with future runbook-mode work.

**Ruling (owner, 11 November 2026)**: the dashboard will carry state-changing
actions, and therefore needs a login again. That reverses the position taken
on 4 August 2026 (commit 701e60d), under which the dashboard is unauthenticated
because it serves only what the chain already publishes. What the login looks
like has not been designed or decided; it is §8 Q1, and no state-changing route
ships before it is designed, ruled and built.

**Components**:
- `internal/chain/client.go` — `GuardianWithdrawStake` client method (missing)
- `internal/cli/` — a `withdraw` verb (CLI parity first; which plan and which
  binary own it is §8 Q2)
- `internal/dashboard/` — the login (§8 Q1); `Actions` interface,
  `POST /api/actions/*` routes; `assets/index.html` + `assets/app.js`
  (actions card, text tokens, footer); the package documentation in
  `dashboard.go` and `handler.go`, which states the unauthenticated, GET-only
  rule
- `internal/guardian/` — `Actions` implementation on the service; action
  submissions recorded into the activity buffers (`observations.go`)
- `internal/monitoring/service.go` — wire the actions provider into the
  dashboard handler, mirroring `SetDashboardSource`
- `internal/config/config.go` — `enable_dashboard_actions` field (§8 Q5), and
  whatever the login needs
- Docs — `timeflareio/chain` — `docs/guides/TESTING_COMMANDS.md` and
  `docs/guides/CONTAINERS.md`, both of which describe the dashboard as
  read-only and unauthenticated. This repository has no operator guide.
- Tests — `internal/dashboard/`, `internal/monitoring/`,
  `internal/guardian/`, `internal/chain/`, `internal/cli/`

**Protocol surface: none.** Every message this plan uses
(`MsgGuardianUpdate`, `MsgGuardianWithdrawStake`) already exists in the wire
contract (`timeflareio/chain` — `proto/timeflare/secrets/v1/tx.proto`); no
proto, `x/secrets/types` or spec change is required.

## 1. What "mirror the CLI exactly" means here

The operator verbs, and where each stands:

| CLI verb | Message | Dashboard action? |
|---|---|---|
| `guardianctl update` (availability window, float deposit, accepting-secrets) | `MsgGuardianUpdate` | **Yes — §3** |
| *(none — gap)* withdraw unlocked float | `MsgGuardianWithdrawStake` | **Yes — §3, after §2 closes the gap** |
| `guardianctl rotate-key` | `MsgGuardianRotateKey` | **No — §4** |
| `guardianctl register` | `MsgGuardianRegister` | **No — §4** |
| `guardianctl wallet …`, `guardianctl key backup / restore` | — (local key material) | **No — §4** |
| *(daemon-automated)* confirm shares, reveal share | `MsgGuardianConfirmShares`, `MsgGuardianRevealShare` | **No — §4** |

"Mirror exactly" is achieved **by construction, not by imitation**:
`guardianctl update` builds `chain.GuardianUpdateOptions` and calls
`chain.Client.GuardianUpdate` (`internal/cli/update.go`,
`internal/chain/client.go:317`). The dashboard actions call **the same client
methods with the same options structs**, so parameter semantics, validation,
signing and broadcast are the CLI's own code path — there is no second
implementation of any transaction to drift.

The execution venue differs in one deliberate way: the CLI constructs its own
short-lived client, while dashboard actions run through the **running
daemon's** client. That is an improvement, not a divergence — the daemon's
signer serialises per-account with a mutex and tracks the sequence locally
(`internal/chain/signer.go`), so an action submitted from the dashboard cannot
race a confirmation or reveal the daemon is signing at the same moment.
Running the CLI beside a live daemon is the path with that race today; the
dashboard path avoids it by sharing the signer.

It also changes what `guardiand` is. Today the daemon signs only the two
protocol duties it automates, and the dashboard package's stated contract is
that "nothing here signs". Actions make the daemon's network-facing listener a
signing surface for `MsgGuardianUpdate` and `MsgGuardianWithdrawStake` — no key
material is written or exported, so the two-binary split's rule is untouched,
but the listener's exposure is no longer that of a read-only page. That is the
reason the login is the gate.

## 2. Close the withdraw gap in the CLI first

`MsgGuardianWithdrawStake` (withdraw the unlocked float; bonds for in-flight
secrets stay locked; the guardian record persists) is reachable today only
via `timeflared tx secrets guardian-withdraw-stake` — a binary the distroless
guardian image does not ship. Neither guardian binary has a withdraw verb and
`chain.Client` has no method for it.

The dashboard cannot mirror a CLI verb that does not exist, so parity work
comes first:

- **`chain.Client.GuardianWithdrawStake(ctx)`** — same shape as the sibling tx
  methods.
- **A `withdraw` verb** — preview-and-confirm flow matching `update` (show the
  current unlocked float being withdrawn, the address it pays to, and that
  locked bonds are untouched).

[PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
adds the same verb, and its open question 1 asks which binary carries it. Who
owns the verb is §8 Q2; this plan needs it to exist, not to be the one that
writes it.

## 3. The actions

Three actions, one execution path. Each is a `POST /api/actions/<name>`
route on the existing dashboard mux, JSON body in, JSON result out.

**`POST /api/actions/update`** — body mirrors the CLI flags, presence-aware
exactly as `MsgGuardianUpdate` is (an omitted field means "no change"):

```json
{
  "available_from": 0,
  "available_until": 28800,
  "deposit_uveil": "5000000000",
  "accepting_secrets": false
}
```

At least one field must be present (the CLI's own rule). `available_from` /
`available_until` are relative blocks from the current height, as on the
wire; the UI preview renders the computed absolute heights beside them, as
the CLI preview does ("Current block + N"). `deposit_uveil` becomes a
`Coin` in the configured denom.

**`POST /api/actions/withdraw`** — empty body. The message has no amount
field: it withdraws the entire unlocked float. The confirm step states the
exact amount about to move (from the same economics snapshot the float panel
renders) and that bonds for in-flight secrets remain locked.

**Accepting-secrets toggle** — not a separate route; it is `update` with
only `accepting_secrets` set. The UI presents it as a switch because it is
the single most-reached-for operational control (pausing intake before
maintenance), but it submits the same message.

**Plumbing** — a `dashboard.Actions` interface beside `Source`, implemented
by the guardian service and wired by the start command through
`monitoring.SetDashboardActions`, the same inversion `SetDashboardSource`
uses (the dashboard package must not import `internal/guardian`). Results
return `{tx_hash}` on success and `{error}` on failure. Every action
submission is also recorded into the existing activity buffer with its own
`SubmissionKind`, so the Transaction outcomes panel shows dashboard-initiated
transactions beside the daemon's own — one audit surface, already built.

The error text needs care. The activity endpoint deliberately serves no error
strings, because a chain rejection or a validation complaint quotes the value
that failed and so carries an endpoint or a path onto the page. What an action
may return to the person who submitted it, behind a login, is part of §8 Q1's
coexistence question rather than something this section settles.

**Confirmation UX mirrors the CLI ceremony.** The CLI never fires on the
first keypress: it previews the exact parameters and asks. The dashboard
does the same — a form, then a confirm step rendering precisely what will
be signed (only the fields being changed, marked as the CLI marks them),
then the result. No action is a single click.

### Security shape

- **The login is undesigned, and it gates everything in this section.** The
  dashboard today has no credential, no TLS and no CSRF defence, and its
  package documentation gives the reason for each: every route is a GET that
  serves chain-public content (`internal/dashboard/dashboard.go`,
  `internal/dashboard/handler.go`). A route that signs has none of those
  properties. §8 Q1 states what the login design must settle; this plan does
  not pre-empt it.
- **Method discipline**: every existing route stays `GET`-only; actions are
  `POST`-only. The handler tests assert both directions across the whole
  mux.
- **`enable_dashboard_actions` config field** (§8 Q5) — an operator may
  want a strictly read-only dashboard on principle. When off, action routes
  answer `403` with a message naming the field, and the UI hides the card.

## 4. Deliberately not dashboard actions

Stated plainly so the boundary is designed rather than accidental:

- **`rotate-key`** — the rotation flow is generate → **passphrase-encrypted
  backup ceremony** → submit, with the hard rule that nothing is broadcast
  before the backup bundle is written and confirmed. That ceremony creates
  and moves key material and a backup file on the guardian host; a browser
  cannot participate in it safely, and a rotation button that skipped the
  ceremony would be a footgun with a 432,000-block cooldown. It is also a
  `guardianctl` verb precisely so that the daemon links no code able to write
  a key. The rotation panel keeps naming the CLI command, and gains nothing
  else.
- **`register`** — a one-time bootstrap ceremony (entry fee, key generation,
  first deposit) that precedes a routinely-running daemon; the dashboard exists
  for day-to-day operation, not first contact.
- **Wallet and share-key management** — key material through a browser is
  out, full stop.
- **Confirm / reveal** — protocol duties the daemon automates. A manual
  path would race the daemon's own decision loop and invite exactly the
  operator error (early reveal → full bond forfeiture) the automation
  exists to prevent.

## 5. UI: the actions card

One new wide card, **Guardian actions**, rather than controls scattered
through the read panels — the read panels' contract (render a snapshot,
invent nothing) stays untouched, and there is a single place where the page
can sign something, which is easier to reason about and to test. The card
holds the availability form, the deposit field, the accepting-secrets
switch and the withdraw control, each with the preview → confirm → outcome
flow of §3. It renders only when actions are enabled and the viewer is logged
in (§8 Q1); otherwise it is absent, not disabled.

The footer opens "This page only reads" and lists `guardianctl update` among
the things only the CLI can do; it also says the page "carries no credential".
Both stop being true when the first action ships, so the footer is rewritten
in the same change to name what remains CLI-only (rotation, registration, key
management, local configuration).

## 6. Presentation change (independent of the actions work)

One owner-requested adjustment with no dependency on the login; it may be
executed as the first phase, ahead of everything above.

- **Slightly lighter text.** The body copy sits on near-black
  (`--tf-ink: #0E0F13`), so "lighter" means brighter: raise the two derived
  text tokens slightly — `--tf-text-muted` from `rgba(244,242,236,.6)` to
  `.7`, `--tf-text-faint` from `.4` to `.48` — leaving primary text
  (`--tf-bone`) and the accent colours untouched. These tokens are copies
  whose provenance comment names
  `timeflareio/mobile-client` — `design/timeflare-brand-1a.md` as the single
  place they are edited, so this is either a brand change or a documented
  dashboard-local divergence — §8 Q4 decides which.

The second adjustment requested on 1 August 2026 — the configuration panel's
detail behind a show/hide control — was written against a panel that listed
every local setting, endpoints and key paths included. That panel is now the
Registration panel and serves only the settings the chain also holds. Whether
anything is left to hide is §8 Q3.

## 7. Implementation phases

1. **Presentation** (§6, independent) — text tokens, provenance comment.
   Browser check on the devnet dashboard.
2. **CLI withdraw parity** (§2) — `chain.Client.GuardianWithdrawStake` and the
   `withdraw` verb with preview/confirm, tests; by whichever plan §8 Q2 gives
   it to.
3. **Login** — designed and ruled under §8 Q1, then built. This plan carries
   no design for it.
4. **Actions plumbing** (§3) — `dashboard.Actions`, POST routes behind the
   login, `SetDashboardActions`, service implementation through the shared
   signer, activity-buffer recording, `enable_dashboard_actions`. Handler
   tests table-driven across every route: GET-only stays GET-only, POST
   routes refuse a request without the login, disabled-actions answers `403`.
   The package documentation in `dashboard.go` and `handler.go` is rewritten
   to describe a listener that both serves the read-only content and signs.
5. **Actions UI** (§5) — the card, preview → confirm → outcome flow,
   footer rewrite.
6. **Docs** — `timeflareio/chain` — `docs/guides/TESTING_COMMANDS.md`
   (withdraw verb, action endpoints) and `docs/guides/CONTAINERS.md` (the
   dashboard section). These describe a released guardian, so they change in
   the chain repository after the guardian release that carries the actions,
   alongside the devnet's `GUARDIAN_VERSION` pin. No spec change — no protocol
   behaviour changes.
7. **Verification** — `make verify && make test` in this repository, `-race`
   across the touched packages; socket-level tests as the listener suite
   does today; then a devnet pass from the chain repository (claim
   `chain.lock` first) against this working tree's build
   (`make dev-up GUARDIAND_BIN=…`): update availability from the dashboard,
   toggle accepting-secrets, deposit, withdraw, and confirm each lands on
   chain with `guardianctl status` and appears in the Transaction outcomes
   panel.

Phases 4–5 land only after phase 3; phases 1–2 have no ordering constraint
against it.

## 8. Open questions

**Q1 — What is the dashboard's login? (gating)** The dashboard will carry
state-changing actions and so needs a login (owner, 11 November 2026); nothing
about it is designed. No state-changing route ships before this is designed,
ruled and built. The design must settle, at least:

- **Credential type and storage** — what an operator presents, and where and
  in what form the daemon holds what it checks against.
- **Transport** — the listener serves plain HTTP today; whether a credential
  may cross it, and what the daemon terminates if not.
- **Coexistence on one listener** — the read-only routes serve only what the
  chain already publishes, and need no credential on that argument. Whether
  they stay open while action routes require the login, whether logging in
  changes what the page may show (action results and chain-side error text
  included), and whether the content rule survives as written.
- **CSRF** — the page has no defence because every route is a GET; that stops
  being true with the first POST.

*Recommendation: none on the design itself, which this plan does not attempt;
rule it before phases 3–5 are scheduled.* The 3 August 2026 arrangement (a
shared password with TLS, `timeflareio/chain` —
`DONE_DASHBOARD_AUTHENTICATION_PLAN.md`) was built and then removed on
4 August 2026 with stated reasons, which the design should answer rather than
repeat.

**Q2 — Which plan owns the `withdraw` verb?** This plan (§2) and
[PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md](PENDING_GUARDIAN_DEAD_CODE_SWEEP_PLAN.md)
both add it. That plan's open question 1 asks which binary carries it and is
not answered here. *Recommendation: the dead-code sweep plan owns the verb and
the client method, since it carries the binary question and does not wait on a
login; this plan's phase 2 becomes a dependency on it.*

**Q3 — Is there still configuration detail to put behind a show/hide
control?** The request was to hide "the important part" of the configuration
panel. The detail it meant — every local setting, endpoints, key paths — is no
longer served at all; the Registration panel shows the on-chain record with
its drift overlay, the availability countdown and the warnings.
*Recommendation: drop the control, and revisit only if Q1's answer brings
host-local settings back onto the page for a logged-in viewer.*

**Q4 — Text tokens: brand-doc change or dashboard-local divergence?**
*Recommendation: dashboard-local*, with the provenance comment amended to
state the divergence and why (a dense monitoring page on a desktop has
different legibility needs from the mobile app). Editing
`timeflare-brand-1a.md` instead would brighten the mobile app's muted text
too, which nobody asked for.

**Q5 — Ship the `enable_dashboard_actions` config field, and with what
default?** The field exists for the operator who wants a read-only dashboard
as policy. *Recommendation: ship it; settle the default with Q1.* Default
`true` makes the feature visible, which is what was asked for; default `false`
keeps an upgraded guardian's listener exactly as exposed as it was. Which is
right depends on what the login requires of an operator before any action can
be submitted.

**Q6 — Is `rotate-key` permanently CLI-only, or does a later plan revisit
it?** *Recommendation: CLI-only until a plan can make the backup ceremony
work without key material crossing the browser* — likely never, and the
rotation panel already names the command. Recorded so the exclusion reads
as designed, not forgotten.

## 9. What this plan does not solve

- **The login's design** — §8 Q1. This plan states that one is required and
  what it must settle, and stops there.
- **Registration, rotation and key management from the dashboard** — §4;
  excluded by design, not deferred by accident.
- **`/metrics` and `/health` exposure** — unchanged by this plan.
- **Remote/multi-guardian operation** — actions drive the one daemon
  serving the page. A fleet runbook surface is a different concern and
  would need its own argued case.
- **Durable action audit history** — action outcomes join the since-start
  activity buffers, which reset on restart like every other panel; a
  durable audit trail remains out of scope for the dashboard.
