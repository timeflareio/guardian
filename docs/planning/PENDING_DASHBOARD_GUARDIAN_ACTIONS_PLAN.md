# Dashboard Guardian Actions — Plan

*Guardian operations from the operator dashboard — availability updates, float
deposits, the accepting-secrets toggle, and withdrawal of the unlocked float —
mirroring exactly what `guardiand` executes from the CLI, plus two dashboard
presentation changes (config detail behind a show/hide control; slightly
lighter text). This is the state-changing successor the dashboard and
authentication plans both deferred: the first slice of "runbook mode".*

> **Status: refining — proposed 1 August 2026.** Open questions in §8 need an
> owner ruling before this is `ready`.
>
> **Priority**: P2 — operator UX. Was hard-gated on
> [`DONE_DASHBOARD_AUTHENTICATION_PLAN.md`](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md)
> (P1) — no state-changing route ships before the dashboard is authenticated —
> and **that gate is satisfied**: authentication merged 3 August 2026, so the
> credential and the `Authenticator` seam this plan extends both exist.
>
> **Origin**: owner request, 1 August 2026 ("perform operations as a guardian
> from the dashboard … mirror exactly we would run from the guardian cli
> tooling … availability, withdrawing [the float], any other operation the
> guardian can action"), discharging the handoff recorded in the
> authentication plan §8 and in
> [`DONE_GUARDIAN_DASHBOARD_PLAN.md`](../done/DONE_GUARDIAN_DASHBOARD_PLAN.md),
> both of which parked state-changing routes with future runbook-mode work.
>
> **Components**:
> - `guardian/blockchain/` — `GuardianWithdrawStake` client method (missing)
> - `guardian/cmd/guardiand/cmd/` — new `withdraw` command (CLI parity first)
> - `guardian/dashboard/` — `Actions` interface, `POST /api/actions/*`
>   routes, same-origin enforcement; `assets/index.html` + `assets/app.js`
>   (actions card, config show/hide, text tokens)
> - `guardian/guardian/` — `Actions` implementation on the service; action
>   submissions recorded into the activity buffers
> - `guardian/monitoring/` — wire the actions provider into the dashboard
>   handler, mirroring `SetDashboardSource`
> - `guardian/config/` — `enable_dashboard_actions` field (§8 Q4)
> - Docs — guardian README, `docs/guides/TESTING_COMMANDS.md`; update the
>   authentication plan §8 and the dashboard plan's runbook-mode references
>   to name this plan
> - Tests — `guardian/dashboard/`, `guardian/monitoring/`,
>   `guardian/guardian/`, `guardian/blockchain/`, `guardian/cmd/`
>
> **Protocol surface: none.** Every message this plan uses
> (`MsgGuardianUpdate`, `MsgGuardianWithdrawStake`) already exists in
> `proto/timeflare/secrets/v1/tx.proto`; no proto, `x/secrets/types` or
> `docs/spec.md` change is required. No new component is introduced — every
> change extends an existing package, binary or page.

## 1. What "mirror the CLI exactly" means here

`guardiand`'s operational verbs, and where each stands:

| CLI verb | Message | Dashboard action? |
|---|---|---|
| `update` (availability window, float deposit, accepting-secrets) | `MsgGuardianUpdate` | **Yes — §3** |
| *(none — gap)* withdraw unlocked float | `MsgGuardianWithdrawStake` | **Yes — §3, after §2 closes the gap** |
| `rotate-key` | `MsgGuardianRotateKey` | **No — §4** |
| `register` | `MsgGuardianRegister` | **No — §4** |
| `wallet create / import / show-address`, `key backup / restore` | — (local key material) | **No — §4** |
| *(daemon-automated)* confirm shares, reveal share | `MsgGuardianConfirmShares`, `MsgGuardianRevealShare` | **No — §4** |

"Mirror exactly" is achieved **by construction, not by imitation**: the CLI's
`update` builds `blockchain.GuardianUpdateOptions` and calls
`blockchain.Client.GuardianUpdate` (`cmd/update.go`, `blockchain/client.go:318`).
The dashboard actions call **the same client methods with the same options
structs**, so parameter semantics, validation, signing and broadcast are the
CLI's own code path — there is no second implementation of any transaction to
drift.

The execution venue differs in one deliberate way: the CLI constructs its own
short-lived client, while dashboard actions run through the **running
daemon's** client. That is an improvement, not a divergence — the daemon's
signer serialises per-account with a mutex and tracks the sequence locally
(`blockchain/signer.go`), so an action submitted from the dashboard cannot
race a confirmation or reveal the daemon is signing at the same moment.
Running the CLI beside a live daemon is the path with that race today; the
dashboard path avoids it by sharing the signer.

## 2. Close the withdraw gap in the CLI first

`MsgGuardianWithdrawStake` (withdraw the unlocked float; bonds for in-flight
secrets stay locked; the guardian record persists) is reachable today only
via `timeflared tx secrets guardian-withdraw-stake` — a binary the distroless
guardian image does not ship, the same failure the `wallet` group was created
to fix. `guardiand` has no withdraw verb and `blockchain.Client` has no
method for it.

The dashboard cannot mirror a CLI verb that does not exist, so parity work
comes first:

- **`blockchain.Client.GuardianWithdrawStake(ctx)`** — same shape as the
  sibling tx methods; the daemon needs it regardless of the dashboard.
- **`guardiand withdraw`** — preview-and-confirm flow matching `update`
  (show the current unlocked float being withdrawn, the address it pays to,
  and that locked bonds are untouched; `--accept` to skip the prompt).

This ordering keeps the owner's framing honest: the dashboard mirrors the
CLI, so the CLI is the reference implementation and gains the verb first.

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
uses (dashboard must not import guardian/guardian). Results return
`{tx_hash}` on success and `{error}` with an honest chain-side message on
failure. Every action submission is also recorded into the existing
activity buffer with its own `kind`, so the Transaction outcomes panel shows
dashboard-initiated transactions beside the daemon's own — one audit
surface, already built.

**Confirmation UX mirrors the CLI ceremony.** The CLI never fires on the
first keypress: it previews the exact parameters and asks. The dashboard
does the same — a form, then a confirm step rendering precisely what will
be signed (only the fields being changed, marked as the CLI marks them),
then the result. No action is a single click.

### Security shape

- **Authentication is inherited, not re-designed.** These routes ship only
  after the authentication plan lands and sit behind the same credential.
  One addition: `POST /api/actions/*` requires the credential
  **unconditionally — loopback included** (§8 Q3). The auth plan's loopback
  exemption is argued from "there is no exposure to defend" for reads; a
  signing surface on `127.0.0.1` is still reachable by every local process
  and every browser tab, so the exemption does not carry over.
- **Cross-site request forgery is live the moment Basic auth meets a POST
  route** — the browser attaches credentials automatically, which is the
  CSRF defence the auth plan explicitly deferred to this plan (its §8).
  Defence in depth, all cheap: reject unless `Sec-Fetch-Site` is
  `same-origin` or `none`; require `Content-Type: application/json`; require
  a custom request header (e.g. `X-Timeflare-Action: 1`) that a cross-site
  form cannot set and whose presence forces a CORS preflight the handler
  never answers. No session state, no token store — consistent with the
  auth plan's rejection of a session model.
- **Method discipline**: every existing route stays `GET`-only; actions are
  `POST`-only. The handler tests assert both directions across the whole
  mux, extending the auth plan's every-route table.
- **`enable_dashboard_actions` config field** (§8 Q4) — an operator may
  want a strictly read-only dashboard on principle. When off, action routes
  answer `403` with a message naming the field, and the UI hides the card.

## 4. Deliberately not dashboard actions

Stated plainly so the boundary is designed rather than accidental:

- **`rotate-key`** — the rotation flow is generate → **passphrase-encrypted
  backup ceremony** → submit, with the hard rule that nothing is broadcast
  before the backup bundle is written and confirmed. That ceremony creates
  and moves key material and a backup file on the guardian host; a browser
  cannot participate in it safely, and a rotation button that skipped the
  ceremony would be a footgun with a 432,000-block cooldown. The rotation
  panel keeps naming the CLI command, and gains nothing else.
- **`register`** — a one-time bootstrap ceremony (entry fee burned, key
  generation, first deposit) that precedes a routinely-running daemon; the
  dashboard exists for day-to-day operation, not first contact.
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
flow of §3. It renders only when actions are enabled and the viewer is
authenticated; otherwise it is absent, not disabled.

The footer's "Read-only. Every action … stays in the CLI." line is rewritten
to name what remains CLI-only (rotation, registration, key management) —
it is currently a promise this plan breaks.

## 6. Presentation changes (independent of the actions work)

Two owner-requested adjustments with no dependency on authentication; they
may be executed as the first phase, ahead of everything above.

- **Config detail behind a show/hide control.** The Configuration panel's
  full fields table (every setting, local vs on-chain, endpoints and key
  paths) collapses behind a toggle styled as a slider switch, hidden by
  default. The summary rows stay always visible: availability window,
  blocks remaining, drift count, validation status, and every warning
  (eligibility, validation, drift) — a hidden panel must never hide a
  problem, and `drift_count` already surfaces disagreement without the
  table. Hiding the detail by default also keeps endpoints and key paths —
  the panel's most targeting-useful content — off a casually shared screen.
  Which rows count as "detail" versus "summary" is confirmed in §8 Q1.
- **Slightly lighter text.** The body copy sits on near-black
  (`--tf-ink: #0E0F13`), so "lighter" means brighter: raise the two derived
  text tokens slightly — `--tf-text-muted` from `rgba(244,242,236,.6)` to
  `.7`, `--tf-text-faint` from `.4` to `.48` — leaving primary text
  (`--tf-bone`) and the accent colours untouched. These tokens are copies
  whose provenance comment names
  `mobile-client/design/timeflare-brand-1a.md` as the single place they are
  edited, so this is either a brand change or a documented dashboard-local
  divergence — §8 Q2 decides which; the recommendation is dashboard-local
  (a dense monitoring page on a desktop has different legibility needs from
  the mobile app), with the provenance comment amended to state the
  divergence and why.

## 7. Implementation phases

1. **Presentation** (§6, independent) — config show/hide control, text
   tokens, provenance comment. Browser check on the devnet dashboard.
2. **CLI withdraw parity** (§2) — `blockchain.Client.GuardianWithdrawStake`,
   `guardiand withdraw` with preview/confirm, tests.
3. **Actions plumbing** (§3) — `dashboard.Actions`, POST routes with the
   full security shape, `SetDashboardActions`, service implementation
   through the shared signer, activity-buffer recording,
   `enable_dashboard_actions`. Handler tests table-driven across every
   route: GET-only stays GET-only, POST routes refuse unauthenticated /
   cross-site / wrong-content-type requests, disabled-actions answers
   `403`.
4. **Actions UI** (§5) — the card, preview → confirm → outcome flow,
   footer rewrite, `401`/`403` rendering distinct from "unavailable".
5. **Docs** — guardian README, `docs/guides/TESTING_COMMANDS.md`
   (withdraw verb, action endpoints); update the authentication plan §8
   and the dashboard plan's runbook-mode hand-off lines to name this plan.
   No `docs/spec.md` change — no protocol behaviour changes.
6. **Verification** — `cd guardian && make verify && make test`, `-race`
   across the touched packages; socket-level tests as the listener suite
   does today; then a devnet pass (claim `chain.lock` first): update
   availability from the dashboard, toggle accepting-secrets, deposit,
   withdraw, and confirm each lands on chain with `guardiand status` and
   appears in the Transaction outcomes panel.

Phases 3–4 land only after the authentication plan is done; phases 1–2 have
no ordering constraint against it.

## 8. Open questions

**Q1 — Config panel: is the summary/detail split right?** Proposed: summary
= availability window, blocks remaining, drift count, validation status,
and all warnings, always visible; detail = the full fields table, hidden by
default behind the slider. *Recommendation: as proposed.* The owner asked
for "the important part" behind the control — confirm the important part is
the detailed table (the sensitive, verbose content) rather than the summary.

**Q2 — Text tokens: brand-doc change or dashboard-local divergence?**
*Recommendation: dashboard-local*, with the provenance comment amended.
Editing `timeflare-brand-1a.md` instead would brighten the mobile app's
muted text too, which nobody asked for.

**Q3 — Do action routes require the credential on loopback, where the
authentication plan exempts reads?** *Recommendation: yes, unconditionally.*
The read exemption is argued from "nothing to defend"; a signing surface on
loopback is still reachable by any local process or browser tab. The cost
is one password prompt for a developer on `127.0.0.1`.

**Q4 — Ship the `enable_dashboard_actions` config field?** *Recommendation:
yes, default `true`.* Authentication already gates the surface; the field
exists for the operator who wants a read-only dashboard as policy. Default
`false` would make the feature invisible-by-default, which defeats the
request.

**Q5 — Is `rotate-key` permanently CLI-only, or does a later plan revisit
it?** *Recommendation: CLI-only until a plan can make the backup ceremony
work without key material crossing the browser* — likely never, and the
rotation panel already names the command. Recorded so the exclusion reads
as designed, not forgotten.

## 9. What this plan does not solve

- **Registration, rotation and key management from the dashboard** — §4;
  excluded by design, not deferred by accident.
- **The authentication model itself** — owned by
  [`DONE_DASHBOARD_AUTHENTICATION_PLAN.md`](../done/DONE_DASHBOARD_AUTHENTICATION_PLAN.md);
  this plan adds the CSRF defence that plan's §8 assigned to its successor,
  and nothing else about access.
- **`/metrics` exposure** — unchanged; still parked with the metrics
  work-stream (authentication plan §6).
- **Remote/multi-guardian operation** — actions drive the one daemon
  serving the page. A fleet runbook surface is a different concern and
  would need its own argued case.
- **Durable action audit history** — action outcomes join the since-start
  activity buffers, which reset on restart like every other panel; a
  durable audit trail remains out of scope for the dashboard.
