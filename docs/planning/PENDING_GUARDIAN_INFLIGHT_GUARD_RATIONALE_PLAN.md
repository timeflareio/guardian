# Guardian in-flight guard — a rationale anchored to a constant that does not exist

| | |
|---|---|
| **Priority** | P3 |
| **Status** | refining |
| **Created** | 6 August 2026 |
| **Updated** | 11 November 2026 |
| **Protocol surface** | none |
| **Repositories** | guardian |
| **Depends on** | — |
| **Related** | [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md); `timeflareio/chain` — `DONE_E2E_SCENARIO_DETERMINISM_PLAN.md` |
| **Open questions** | 1 (Open questions) |

**Why this priority**: the guard's *value* is safe; only its stated justification is
wrong. But the wrong justification names a protocol window, and a comment is
where a later reader looks first, which is how a consumer's guess about the
chain becomes something that reader trusts.
**Status notes**: one open question below.
**Origin**: found on 6 August 2026 while validating candidate-pool parking for
`timeflareio/chain` — `DONE_E2E_SCENARIO_DETERMINISM_PLAN.md`.
**Components**: `internal/guardian/inflight.go`.

## The problem

`internal/guardian/inflight.go` sets the duplicate-submission guard's expiry and
justifies the number like this:

> Inclusion normally takes one block, so five absorbs ordinary congestion while
> leaving fifteen of the twenty-block MinCommitTimeout for a genuine retry if
> the transaction was dropped from the mempool rather than included.

**There is no `MinCommitTimeout`** — not in this daemon, not in the wire contract
(`chain/x/secrets/types`), not anywhere. And the window it is reaching for,
`CommitTimeoutBlocks`, is **50 blocks, not 20**. So the arithmetic in the comment
("fifteen of the twenty") describes a protocol that does not exist.

The value itself is fine and is not in question: `inFlightExpiryBlocks = 5` leaves
45 of the real 50-block window for a genuine retry, which is *more* headroom than
the comment claims, not less. Nothing misbehaves today.

Two things make it worth fixing rather than leaving:

1. **It is a consumer inventing a protocol fact.** The workspace rule is explicit
   that protocol behaviour is never inferred from a consumer's code, because the
   code may be what is wrong — and here it is. A reader tuning this value would
   derive their new number from 20.
2. **The belief spreads.** A plan that reasons about the commit window while
   reading this comment inherits the name and the number:
   [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
   phase 5, explaining why `polling_interval` is *not* bounded against a commit
   window, has to cite the window, and cites `CommitTimeoutBlocks`. The comment
   is the one place in this repository's code that still says otherwise.

## What the guard actually has to satisfy

Worth writing down, because the corrected comment should state the real bounds
rather than a repaired version of the wrong ones:

- **Above inclusion latency.** A broadcast returns at CheckTx, not at inclusion,
  so the guard exists to stop a poll cycle re-submitting work that is already in
  the mempool. Inclusion normally takes one block, so the floor is "comfortably
  more than one".
- **Below the commit window**, `CommitTimeoutBlocks` = 50 blocks. A transaction
  dropped from the mempool rather than included must become eligible again with
  enough of the window left to land a replacement.
- **Below the reveal path's evict-and-re-add recovery**, or the guard converts a
  case that self-corrects into a missed reveal window — which costs 40% of the
  bond burned plus 10% to the creator, far more than the duplicate fee it saves.
  This bound is the binding one and the comment already names it correctly.

**Cadence matters, and in the direction that is easy to get backwards.** The guard
is denominated in blocks, so it is invariant to cadence by construction — but the
hazard it protects against is a *wall-clock* stall, and a fixed stall spans more
blocks on a faster chain. Five blocks is 30 seconds at the shipping 6s cadence and
1.25 seconds on a devnet at 200ms, so the guard is tightest on the fastest chain
rather than the slowest. Empirically it holds there: the chain's S10b scenario,
which exists to catch a guardian paying for a duplicate acceptance, was clean
across eight full suite runs at a measured 227ms cadence on 6 August 2026.

## Execution

1. Rewrite the rationale in `internal/guardian/inflight.go` against the real
   window: `CommitTimeoutBlocks` = 50, the three bounds above, and the cadence
   note. Present tense, describing the code as it stands — no mention of the
   figure that was there before (git history carries that).
2. Grep for `MinCommitTimeout` across this repository's code and confirm
   `inflight.go` is the only site, per the cross-component sweep rule. This
   plan names it to describe the defect and is left alone.
3. `make verify && make test`. No behaviour changes, so no new test is warranted —
   the value is untouched.

## What this plan does not solve

- **It does not retune the guard.** Five blocks stays. The correction makes the
  real headroom visible (45 of 50 blocks rather than 15 of 20), which is an
  argument for leaving the value alone, not for changing it.
- **It does not add a bound on `polling_interval`.** That remains deliberately
  absent for the reasons
  [PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md](PENDING_GUARDIAN_CONFIG_SAFETY_PLAN.md)
  phase 5 gives.
- **It does not audit every other prose restatement of a protocol number in this
  daemon.** Only the comment naming this one. A broader sweep for hard-coded
  protocol facts in comments would be its own piece of work, and open question 1
  is the narrow version of that question.

## Open questions

**1. Should the comment name the number at all, or reference the constant?**

The drift happened because a protocol window was restated as prose. The daemon
already imports `chain/x/secrets/types` — the wire contract — so it *could* say
`types.CommitTimeoutBlocks` and be wrong-proof: if the chain retunes the window,
the reference follows and the reasoning stays true.

Against that: `inFlightExpiryBlocks` is the guardian's own tuning decision, not a
derived quantity, and turning a comment into a code reference to justify a literal
is a slightly odd shape. The alternative is to keep the comment qualitative —
"well inside the commit window, and below the reveal path's recovery" — and name
no numbers at all, which cannot drift because it asserts nothing numeric.

**Recommendation: keep it qualitative, and name `CommitTimeoutBlocks` by symbol
rather than by value.** That gets the wrong-proofing without pretending the 5 is
derived: the comment says which window bounds it and lets the reader look up the
number in the one place that owns it. Making the *value* a computed fraction of
the constant would be the over-engineered version and is not proposed — the guard
wants a small fixed number of blocks, not a ratio.
